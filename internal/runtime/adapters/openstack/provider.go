package openstack

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/attributestags"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
)

// Ключи metadata и тегов, которыми помечаются ресурсы рантайма.
const (
	keyRuntime   = "cellestial_runtime"
	keyEnv       = "cellestial_env"
	keySession   = "session_id"
	keyPool      = "pool_id"
	keyOperation = "operation_id"

	kindServer = "server"
	kindVolume = "volume"
	kindPort   = "port"
)

var _ usecase.CloudProvider = (*Provider)(nil)

// Provider реализует usecase.CloudProvider поверх Nova, Cinder и Neutron.
type Provider struct {
	compute, volume, network *gophercloud.ServiceClient
	cfg                      config.RuntimeConfig
	poll                     time.Duration
}

func newProvider(compute, volume, network *gophercloud.ServiceClient, cfg config.RuntimeConfig) *Provider {
	return &Provider{compute: compute, volume: volume, network: network, cfg: cfg, poll: defaultPoll}
}

// owner — владелец ресурса по metadata: сессия или VPS пула (ровно одно из полей).
type owner struct{ session, pool uuid.UUID }

func ownerOf(env domain.Environment) owner {
	if env.IsPoolVPS() {
		return owner{pool: env.SessionID}
	}
	return owner{session: env.SessionID}
}

// ownerKey — ключ metadata и тега владельца: pool_id у VPS пула, session_id у VPS сессии.
func ownerKey(env domain.Environment) string {
	if env.IsPoolVPS() {
		return keyPool
	}
	return keySession
}

func resourceName(env domain.Environment) string {
	if env.IsPoolVPS() {
		return "runtime-pool-" + env.SessionID.String()
	}
	return "runtime-" + env.SessionID.String()
}

func (p *Provider) metadata(env domain.Environment) map[string]string {
	return map[string]string{
		keyRuntime:    "1",
		keyEnv:        p.cfg.EnvName,
		ownerKey(env): env.SessionID.String(),
		keyOperation:  env.OperationID.String(),
	}
}

// owned проверяет metadata ресурса: он создан рантаймом этого окружения; возвращает владельца.
func (p *Provider) owned(md map[string]string) (owner, bool) {
	if md[keyRuntime] != "1" || md[keyEnv] != p.cfg.EnvName {
		return owner{}, false
	}
	if id, err := uuid.Parse(md[keyPool]); err == nil {
		return owner{pool: id}, true
	}
	id, err := uuid.Parse(md[keySession])
	return owner{session: id}, err == nil
}

func (p *Provider) portTags(env domain.Environment) []string {
	return []string{keyRuntime + "=1", keyEnv + "=" + p.cfg.EnvName, ownerKey(env) + "=" + env.SessionID.String()}
}

func (p *Provider) portDescription(env domain.Environment) string {
	return fmt.Sprintf("%s=1 %s=%s %s=%s %s=%s", keyRuntime, keyEnv, p.cfg.EnvName,
		ownerKey(env), env.SessionID, keyOperation, env.OperationID)
}

// ownedPort разбирает description порта и его теги (теги имеют приоритет): порт мог остаться
// без тегов после сбоя между созданием и PUT tags.
func (p *Provider) ownedPort(port ports.Port) (owner, bool) {
	md := map[string]string{}
	for _, t := range append(strings.Fields(port.Description), port.Tags...) {
		if k, v, ok := strings.Cut(t, "="); ok {
			md[k] = v
		}
	}
	return p.owned(md)
}

// wait опрашивает check с интервалом p.poll, пока он не вернёт done или ошибку, либо до отмены ctx.
func (p *Provider) wait(ctx context.Context, op string, check func() (bool, error)) error {
	for {
		done, err := check()
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return classify(op, ctx.Err())
		case <-time.After(p.poll):
		}
	}
}

// EnsurePort находит порт сессии (по ID, тегам или имени с description) либо создаёт его в
// RUNTIME_SUBNET_ID с RUNTIME_SECURITY_GROUP_ID и помечает тегами.
func (p *Provider) EnsurePort(ctx context.Context, env domain.Environment) (string, string, error) {
	if id := env.Resources.PortID; id != "" {
		port, err := ports.Get(ctx, p.network, id).Extract()
		switch {
		case err == nil:
			return p.tagPort(ctx, *port, env)
		case !isNotFound(err):
			return "", "", classify("get port", err)
		}
	}
	found, err := p.listPorts(ctx, ports.ListOpts{Tags: strings.Join(p.portTags(env), ",")})
	if err != nil {
		return "", "", err
	}
	if len(found) == 0 {
		// Порт мог быть создан, но не помечен тегами (сбой между POST и PUT tags).
		byName, err := p.listPorts(ctx, ports.ListOpts{Name: resourceName(env), NetworkID: p.cfg.NetworkID})
		if err != nil {
			return "", "", err
		}
		for _, port := range byName {
			if o, ok := p.ownedPort(port); ok && o == ownerOf(env) {
				found = append(found, port)
			}
		}
	}
	var port ports.Port
	if len(found) > 0 {
		port = found[0]
	} else {
		sg := []string{p.cfg.SecurityGroupID}
		created, err := ports.Create(ctx, p.network, ports.CreateOpts{
			NetworkID:      p.cfg.NetworkID,
			Name:           resourceName(env),
			Description:    p.portDescription(env),
			FixedIPs:       []ports.IP{{SubnetID: p.cfg.SubnetID}},
			SecurityGroups: &sg,
		}).Extract()
		if err != nil {
			return "", "", classify("create port", err)
		}
		port = *created
	}
	return p.tagPort(ctx, port, env)
}

// tagPort дописывает порту недостающие теги рантайма (например, после сбоя между POST и PUT tags),
// сохраняя прочие теги.
func (p *Provider) tagPort(ctx context.Context, port ports.Port, env domain.Environment) (string, string, error) {
	have := map[string]bool{}
	for _, t := range port.Tags {
		have[t] = true
	}
	tags := append([]string(nil), port.Tags...)
	for _, t := range p.portTags(env) {
		if !have[t] {
			tags = append(tags, t)
		}
	}
	if len(tags) != len(port.Tags) {
		_, err := attributestags.ReplaceAll(ctx, p.network, "ports", port.ID,
			attributestags.ReplaceAllOpts{Tags: tags}).Extract()
		if err != nil {
			return port.ID, p.portIP(port), classify("tag port", err)
		}
	}
	return port.ID, p.portIP(port), nil
}

func (p *Provider) portIP(port ports.Port) string {
	for _, ip := range port.FixedIPs {
		if ip.SubnetID == p.cfg.SubnetID {
			return ip.IPAddress
		}
	}
	if len(port.FixedIPs) > 0 {
		return port.FixedIPs[0].IPAddress
	}
	return ""
}

func (p *Provider) listPorts(ctx context.Context, opts ports.ListOpts) ([]ports.Port, error) {
	pages, err := ports.List(p.network, opts).AllPages(ctx)
	if err != nil {
		return nil, classify("list ports", err)
	}
	list, err := ports.ExtractPorts(pages)
	if err != nil {
		return nil, classify("list ports", err)
	}
	return list, nil
}

// EnsureVolume находит загрузочный том сессии (по ID или metadata) либо создаёт его из
// RUNTIME_IMAGE_ID и ждёт статуса available. При ошибке ожидания ID тома тоже возвращается.
func (p *Provider) EnsureVolume(ctx context.Context, env domain.Environment) (string, error) {
	id := ""
	if known := env.Resources.VolumeID; known != "" {
		_, err := volumes.Get(ctx, p.volume, known).Extract()
		switch {
		case err == nil:
			id = known
		case !isNotFound(err):
			return "", classify("get volume", err)
		}
	}
	if id == "" {
		found, err := p.listVolumes(ctx, map[string]string{keyEnv: p.cfg.EnvName, ownerKey(env): env.SessionID.String()})
		if err != nil {
			return "", err
		}
		for _, v := range found {
			if o, ok := p.owned(v.Metadata); ok && o == ownerOf(env) {
				id = v.ID
				break
			}
		}
	}
	if id == "" {
		v, err := volumes.Create(ctx, p.volume, volumes.CreateOpts{
			Name:             resourceName(env),
			Size:             p.cfg.RootDiskGiB,
			ImageID:          p.cfg.ImageID,
			VolumeType:       p.cfg.VolumeType,
			AvailabilityZone: p.cfg.AvailabilityZone,
			Metadata:         p.metadata(env),
		}, nil).Extract()
		if err != nil {
			return "", classify("create volume", err)
		}
		id = v.ID
	}
	err := p.wait(ctx, "wait volume", func() (bool, error) {
		v, err := volumes.Get(ctx, p.volume, id).Extract()
		if err != nil {
			return false, classify("get volume", err)
		}
		switch v.Status {
		case "available":
			return true, nil
		case "error", "error_restoring", "error_extending":
			return false, fmt.Errorf("openstack volume %s status %s: %w", id, v.Status, usecase.ErrCloudPermanent)
		}
		return false, nil
	})
	return id, err
}

func (p *Provider) listVolumes(ctx context.Context, md map[string]string) ([]volumes.Volume, error) {
	pages, err := volumes.List(p.volume, volumes.ListOpts{Metadata: md}).AllPages(ctx)
	if err != nil {
		return nil, classify("list volumes", err)
	}
	list, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return nil, classify("list volumes", err)
	}
	return list, nil
}

// EnsureServer находит VPS сессии (по ID или имени и metadata) либо создаёт её с загрузкой с тома
// env.Resources.VolumeID и портом env.Resources.PortID, затем ждёт ACTIVE. Статус ERROR —
// ErrCloudPermanent. При ошибке ожидания ID сервера тоже возвращается.
func (p *Provider) EnsureServer(ctx context.Context, env domain.Environment, userData string) (string, error) {
	id := ""
	if known := env.Resources.ServerID; known != "" {
		_, err := servers.Get(ctx, p.compute, known).Extract()
		switch {
		case err == nil:
			id = known
		case !isNotFound(err):
			return "", classify("get server", err)
		}
	}
	if id == "" {
		found, err := p.listServers(ctx, servers.ListOpts{Name: "^" + resourceName(env) + "$"})
		if err != nil {
			return "", err
		}
		for _, s := range found {
			if o, ok := p.owned(s.Metadata); ok && o == ownerOf(env) {
				id = s.ID
				break
			}
		}
	}
	if id == "" {
		if env.Resources.VolumeID == "" || env.Resources.PortID == "" {
			return "", fmt.Errorf("openstack create server: volume and port are required: %w", usecase.ErrCloudPermanent)
		}
		s, err := servers.Create(ctx, p.compute, keypairs.CreateOptsExt{
			KeyName: p.cfg.SSHKeyName,
			CreateOptsBuilder: servers.CreateOpts{
				Name:             resourceName(env),
				FlavorRef:        p.cfg.FlavorID,
				AvailabilityZone: p.cfg.AvailabilityZone,
				Networks:         []servers.Network{{Port: env.Resources.PortID}},
				Metadata:         p.metadata(env),
				UserData:         []byte(base64.StdEncoding.EncodeToString([]byte(userData))),
				BlockDevice: []servers.BlockDevice{{
					UUID:                env.Resources.VolumeID,
					SourceType:          servers.SourceVolume,
					DestinationType:     servers.DestinationVolume,
					BootIndex:           0,
					DeleteOnTermination: true,
				}},
			},
		}, nil).Extract()
		if err != nil {
			return "", classify("create server", err)
		}
		id = s.ID
	}
	err := p.wait(ctx, "wait server", func() (bool, error) {
		s, err := servers.Get(ctx, p.compute, id).Extract()
		if err != nil {
			return false, classify("get server", err)
		}
		switch s.Status {
		case "ACTIVE":
			return true, nil
		case "ERROR":
			return false, fmt.Errorf("openstack server %s status ERROR: %s: %w", id, s.Fault.Message, usecase.ErrCloudPermanent)
		}
		return false, nil
	})
	return id, err
}

func (p *Provider) listServers(ctx context.Context, opts servers.ListOpts) ([]servers.Server, error) {
	pages, err := servers.List(p.compute, opts).AllPages(ctx)
	if err != nil {
		return nil, classify("list servers", err)
	}
	list, err := servers.ExtractServers(pages)
	if err != nil {
		return nil, classify("list servers", err)
	}
	return list, nil
}

// deleteAndWait удаляет ресурс и ждёт 404. 404 при удалении — успех.
func (p *Provider) deleteAndWait(ctx context.Context, kind string, del func() error, get func() error) error {
	if err := del(); err != nil {
		if isNotFound(err) {
			return nil
		}
		return classify("delete "+kind, err)
	}
	return p.wait(ctx, "wait "+kind+" deletion", func() (bool, error) {
		err := get()
		if err == nil {
			return false, nil
		}
		if isNotFound(err) {
			return true, nil
		}
		return false, classify("get "+kind, err)
	})
}

// DeleteServer удаляет VPS и ждёт её исчезновения.
func (p *Provider) DeleteServer(ctx context.Context, id string) error {
	return p.deleteAndWait(ctx, kindServer,
		func() error { return servers.Delete(ctx, p.compute, id).ExtractErr() },
		func() error { _, err := servers.Get(ctx, p.compute, id).Extract(); return err })
}

// DeleteVolume удаляет том и ждёт его исчезновения. 400 (том ещё отсоединяется или занят) —
// временная ошибка.
func (p *Provider) DeleteVolume(ctx context.Context, id string) error {
	return p.deleteAndWait(ctx, kindVolume,
		func() error {
			err := volumes.Delete(ctx, p.volume, id, volumes.DeleteOpts{}).ExtractErr()
			if gophercloud.ResponseCodeIs(err, 400) && !strings.Contains(strings.ToLower(err.Error()), "quota") {
				return fmt.Errorf("openstack delete volume %s: volume is busy: %w", id, usecase.ErrCloudTransient)
			}
			return err
		},
		func() error { _, err := volumes.Get(ctx, p.volume, id).Extract(); return err })
}

// DeletePort удаляет порт и ждёт его исчезновения.
func (p *Provider) DeletePort(ctx context.Context, id string) error {
	return p.deleteAndWait(ctx, kindPort,
		func() error { return ports.Delete(ctx, p.network, id).ExtractErr() },
		func() error { _, err := ports.Get(ctx, p.network, id).Extract(); return err })
}

// ListOwned возвращает ресурсы с metadata/тегами cellestial_runtime=1 и cellestial_env=RUNTIME_ENV_NAME.
func (p *Provider) ListOwned(ctx context.Context) ([]usecase.CloudResource, error) {
	var out []usecase.CloudResource
	srvs, err := p.listServers(ctx, servers.ListOpts{})
	if err != nil {
		return nil, err
	}
	for _, s := range srvs {
		if o, ok := p.owned(s.Metadata); ok {
			out = append(out, usecase.CloudResource{Kind: kindServer, ID: s.ID, SessionID: o.session, PoolID: o.pool})
		}
	}
	vols, err := p.listVolumes(ctx, map[string]string{keyRuntime: "1", keyEnv: p.cfg.EnvName})
	if err != nil {
		return nil, err
	}
	for _, v := range vols {
		if o, ok := p.owned(v.Metadata); ok {
			out = append(out, usecase.CloudResource{Kind: kindVolume, ID: v.ID, SessionID: o.session, PoolID: o.pool})
		}
	}
	tagged, err := p.listPorts(ctx, ports.ListOpts{Tags: keyRuntime + "=1," + keyEnv + "=" + p.cfg.EnvName})
	if err != nil {
		return nil, err
	}
	// Порты без тегов (сбой между POST и PUT tags) находятся по description в сети рантайма.
	inNetwork, err := p.listPorts(ctx, ports.ListOpts{NetworkID: p.cfg.NetworkID})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, port := range append(tagged, inNetwork...) {
		if seen[port.ID] {
			continue
		}
		if o, ok := p.ownedPort(port); ok {
			seen[port.ID] = true
			out = append(out, usecase.CloudResource{Kind: kindPort, ID: port.ID, SessionID: o.session, PoolID: o.pool})
		}
	}
	return out, nil
}
