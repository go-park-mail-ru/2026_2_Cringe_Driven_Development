package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// RuntimeConfig — настройки создания VPS пользовательских сессий (RUNTIME_*).
// Обязательные поля проверяются при каждом запуске API.
type RuntimeConfig struct {
	Provider string // openstack | fake

	OSAuthURL, OSUsername, OSPassword, OSUserDomainName string
	OSProjectID, OSRegion, AvailabilityZone             string
	NetworkID, SubnetID, SecurityGroupID                string
	ImageID, FlavorID, VolumeType                       string
	SSHKeyName, SSHUser, SSHPrivateKeyFile              string
	TLSCACertFile, TLSCAKeyFile                         string
	TLSClientCertFile, TLSClientKeyFile                 string
	WorkDir                                             string

	SecretsKey       []byte
	RootDiskGiB      int
	KernelDNS        string
	BlockedPublicIPs []string
	EnvName          string
	PythonImage      string
	AgentVersion     string
	// WarmPoolSize — число заранее подготовленных VPS (RUNTIME_WARM_POOL_SIZE); 0 выключает пул.
	WarmPoolSize int

	StartupTimeout, AnsibleTimeout, DrainTimeout time.Duration
	DeleteTimeout, ReconcileInterval             time.Duration
	MaxConcurrentOps, MaxActive                  int
}

func loadRuntime(lookup func(string) (string, bool)) (RuntimeConfig, error) {
	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	r := RuntimeConfig{Provider: get("RUNTIME_PROVIDER", "")}
	var bad error
	fail := func(key, what string) {
		if bad == nil {
			bad = fmt.Errorf("invalid %s: %s", key, what)
		}
	}
	integer := func(key string, fallback int) int {
		raw := get(key, "")
		if raw == "" {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			fail(key, "expected a positive integer")
			return fallback
		}
		return n
	}
	dur := func(key string, fallback time.Duration) time.Duration {
		raw := get(key, "")
		if raw == "" {
			return fallback
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			fail(key, "expected a positive duration")
			return fallback
		}
		return d
	}

	r.OSAuthURL, r.OSUsername, r.OSPassword = get("RUNTIME_OS_AUTH_URL", ""), get("RUNTIME_OS_USERNAME", ""), get("RUNTIME_OS_PASSWORD", "")
	r.OSUserDomainName, r.OSProjectID, r.OSRegion = get("RUNTIME_OS_USER_DOMAIN_NAME", ""), get("RUNTIME_OS_PROJECT_ID", ""), get("RUNTIME_OS_REGION", "")
	r.AvailabilityZone = get("RUNTIME_AVAILABILITY_ZONE", "")
	r.NetworkID, r.SubnetID, r.SecurityGroupID = get("RUNTIME_NETWORK_ID", ""), get("RUNTIME_SUBNET_ID", ""), get("RUNTIME_SECURITY_GROUP_ID", "")
	r.ImageID, r.FlavorID, r.VolumeType = get("RUNTIME_IMAGE_ID", ""), get("RUNTIME_FLAVOR_ID", ""), get("RUNTIME_VOLUME_TYPE", "")
	r.SSHKeyName, r.SSHUser, r.SSHPrivateKeyFile = get("RUNTIME_SSH_KEY_NAME", ""), get("RUNTIME_SSH_USER", "root"), get("RUNTIME_SSH_PRIVATE_KEY_FILE", "")
	r.TLSCACertFile, r.TLSCAKeyFile = get("RUNTIME_TLS_CA_CERT_FILE", ""), get("RUNTIME_TLS_CA_KEY_FILE", "")
	r.TLSClientCertFile, r.TLSClientKeyFile = get("RUNTIME_TLS_CLIENT_CERT_FILE", ""), get("RUNTIME_TLS_CLIENT_KEY_FILE", "")
	r.WorkDir = get("RUNTIME_WORK_DIR", "/var/lib/cellestial/runtime")
	r.KernelDNS = get("RUNTIME_KERNEL_DNS", "1.1.1.1")
	r.EnvName = get("RUNTIME_ENV_NAME", "")
	r.PythonImage = get("RUNTIME_PYTHON_IMAGE", "cellestial-kernel-python:local")
	r.AgentVersion = get("RUNTIME_AGENT_VERSION", "dev")
	r.RootDiskGiB = integer("RUNTIME_ROOT_DISK_GIB", 20)
	r.MaxConcurrentOps = integer("RUNTIME_MAX_CONCURRENT_OPS", 2)
	r.MaxActive = integer("RUNTIME_MAX_ACTIVE", 3)
	r.WarmPoolSize = 2
	if raw := get("RUNTIME_WARM_POOL_SIZE", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			fail("RUNTIME_WARM_POOL_SIZE", "expected a non-negative integer")
		}
		r.WarmPoolSize = n
	}
	r.StartupTimeout = dur("RUNTIME_STARTUP_TIMEOUT", 20*time.Minute)
	r.AnsibleTimeout = dur("RUNTIME_ANSIBLE_TIMEOUT", 15*time.Minute)
	r.DrainTimeout = dur("RUNTIME_DRAIN_TIMEOUT", 30*time.Second)
	r.DeleteTimeout = dur("RUNTIME_DELETE_TIMEOUT", 10*time.Minute)
	r.ReconcileInterval = dur("RUNTIME_RECONCILE_INTERVAL", 5*time.Minute)
	if bad != nil {
		return RuntimeConfig{}, bad
	}

	if r.Provider != "openstack" && r.Provider != "fake" {
		return RuntimeConfig{}, errors.New("invalid RUNTIME_PROVIDER: expected openstack or fake")
	}
	key, err := base64.StdEncoding.DecodeString(get("RUNTIME_SECRETS_KEY", ""))
	if err != nil || len(key) != 32 {
		return RuntimeConfig{}, errors.New("invalid RUNTIME_SECRETS_KEY: expected base64 of exactly 32 bytes")
	}
	r.SecretsKey = key
	// Для openstack имя окружения обязательно: по метке cellestial_env воркер находит и удаляет
	// свои ресурсы, и ошибочное умолчание задело бы ресурсы другого окружения.
	if r.Provider == "fake" && r.EnvName == "" {
		r.EnvName = "dev-fake"
	}

	if r.Provider == "openstack" {
		for _, req := range []struct{ name, value string }{
			{"RUNTIME_ENV_NAME", r.EnvName},
			{"RUNTIME_OS_AUTH_URL", r.OSAuthURL}, {"RUNTIME_OS_USERNAME", r.OSUsername}, {"RUNTIME_OS_PASSWORD", r.OSPassword},
			{"RUNTIME_OS_USER_DOMAIN_NAME", r.OSUserDomainName}, {"RUNTIME_OS_PROJECT_ID", r.OSProjectID}, {"RUNTIME_OS_REGION", r.OSRegion},
			{"RUNTIME_AVAILABILITY_ZONE", r.AvailabilityZone}, {"RUNTIME_NETWORK_ID", r.NetworkID}, {"RUNTIME_SUBNET_ID", r.SubnetID},
			{"RUNTIME_SECURITY_GROUP_ID", r.SecurityGroupID}, {"RUNTIME_IMAGE_ID", r.ImageID}, {"RUNTIME_FLAVOR_ID", r.FlavorID},
			{"RUNTIME_VOLUME_TYPE", r.VolumeType}, {"RUNTIME_SSH_KEY_NAME", r.SSHKeyName}, {"RUNTIME_SSH_PRIVATE_KEY_FILE", r.SSHPrivateKeyFile},
			{"RUNTIME_TLS_CA_CERT_FILE", r.TLSCACertFile}, {"RUNTIME_TLS_CA_KEY_FILE", r.TLSCAKeyFile},
			{"RUNTIME_TLS_CLIENT_CERT_FILE", r.TLSClientCertFile}, {"RUNTIME_TLS_CLIENT_KEY_FILE", r.TLSClientKeyFile},
		} {
			if req.value == "" {
				return RuntimeConfig{}, fmt.Errorf("%s is not set", req.name)
			}
		}
		for _, part := range strings.Split(get("RUNTIME_BLOCKED_PUBLIC_IPS", ""), ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if net.ParseIP(part) == nil {
				return RuntimeConfig{}, errors.New("invalid RUNTIME_BLOCKED_PUBLIC_IPS: expected comma-separated IP addresses")
			}
			r.BlockedPublicIPs = append(r.BlockedPublicIPs, part)
		}
		if len(r.BlockedPublicIPs) == 0 {
			return RuntimeConfig{}, errors.New("RUNTIME_BLOCKED_PUBLIC_IPS is not set")
		}
	}
	return r, nil
}
