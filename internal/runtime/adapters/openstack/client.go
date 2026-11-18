// Package openstack создаёт и удаляет VPS пользовательских сессий в Selectel через OpenStack API
// (Nova, Cinder, Neutron) на Gophercloud v2.
package openstack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/gophercloud/gophercloud/v2"
	gopenstack "github.com/gophercloud/gophercloud/v2/openstack"
)

const defaultPoll = 5 * time.Second

// New авторизуется в Keystone v3 паролем сервисного пользователя (с переавторизацией по истечении
// токена) и получает endpoints Nova, Cinder и Neutron из каталога для cfg.OSRegion.
func New(ctx context.Context, cfg config.RuntimeConfig) (*Provider, error) {
	pc, err := gopenstack.AuthenticatedClient(ctx, gophercloud.AuthOptions{
		IdentityEndpoint: cfg.OSAuthURL,
		Username:         cfg.OSUsername,
		Password:         cfg.OSPassword,
		DomainName:       cfg.OSUserDomainName,
		TenantID:         cfg.OSProjectID,
		AllowReauth:      true,
	})
	if err != nil {
		return nil, classify("authenticate", err)
	}
	eo := gophercloud.EndpointOpts{Region: cfg.OSRegion}
	compute, err := gopenstack.NewComputeV2(pc, eo)
	if err != nil {
		return nil, classify("compute endpoint", err)
	}
	volume, err := gopenstack.NewBlockStorageV3(pc, eo)
	if err != nil {
		return nil, classify("block storage endpoint", err)
	}
	network, err := gopenstack.NewNetworkV2(pc, eo)
	if err != nil {
		return nil, classify("network endpoint", err)
	}
	return newProvider(compute, volume, network, cfg), nil
}

// classify оборачивает ошибку OpenStack в usecase.ErrCloudTransient или usecase.ErrCloudPermanent.
// Текст ответа облака сокращается; учётные данные в запросах не попадают в ошибку.
func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, usecase.ErrCloudTransient) || errors.Is(err, usecase.ErrCloudPermanent) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("openstack %s: %w: %w", op, usecase.ErrCloudTransient, err)
	}
	var code gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &code) {
		body := strings.TrimSpace(string(code.Body))
		if len(body) > 300 {
			body = body[:300] + "…"
		}
		msg := fmt.Sprintf("openstack %s: HTTP %d", op, code.Actual)
		if body != "" {
			msg += ": " + body
		}
		lower := strings.ToLower(body)
		switch {
		case strings.Contains(lower, "quota") || strings.Contains(lower, "limitexceeded"):
			return fmt.Errorf("%s: %w", msg, usecase.ErrCloudPermanent)
		case code.Actual >= 500, code.Actual == http.StatusTooManyRequests,
			code.Actual == http.StatusRequestTimeout, code.Actual == http.StatusConflict:
			return fmt.Errorf("%s: %w", msg, usecase.ErrCloudTransient)
		default:
			return fmt.Errorf("%s: %w", msg, usecase.ErrCloudPermanent)
		}
	}
	var timeout gophercloud.ErrTimeOut
	var netErr net.Error
	if errors.As(err, &timeout) || errors.As(err, &netErr) {
		return fmt.Errorf("openstack %s: %w: %w", op, usecase.ErrCloudTransient, err)
	}
	var unauth gophercloud.ErrUnableToReauthenticate
	if errors.As(err, &unauth) {
		return fmt.Errorf("openstack %s: reauthentication failed: %w", op, usecase.ErrCloudPermanent)
	}
	var endpoint *gophercloud.ErrEndpointNotFound
	var endpointVal gophercloud.ErrEndpointNotFound
	if errors.As(err, &endpoint) || errors.As(err, &endpointVal) {
		return fmt.Errorf("openstack %s: %w: %w", op, usecase.ErrCloudPermanent, err)
	}
	// Прочие ошибки транспорта (обрыв соединения, неожиданный EOF) считаем временными.
	return fmt.Errorf("openstack %s: %w: %w", op, usecase.ErrCloudTransient, err)
}

// isNotFound распознаёт отсутствие ресурса по HTTP-ошибке OpenStack.
func isNotFound(err error) bool { return gophercloud.ResponseCodeIs(err, http.StatusNotFound) }
