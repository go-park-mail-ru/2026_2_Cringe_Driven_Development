package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
)

// Files передаёт запрос по mTLS. Тело остаётся потоком до закрытия вызывающим кодом.
func (c *Client) Files(ctx context.Context, ep U.AgentEndpoint, input F.FileRequest) (F.FileResponse, error) {
	if !strings.HasPrefix(input.Path, "/workspace/") || strings.Contains(input.Path, "..") {
		return F.FileResponse{}, D.ErrInvalid
	}
	target := ep.BaseURL + input.Path
	if input.Query != "" {
		target += "?" + input.Query
	}
	req, err := http.NewRequestWithContext(ctx, input.Method, target, input.Body)
	if err != nil {
		return F.FileResponse{}, D.ErrInvalid
	}
	req.ContentLength = input.Length
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("X-Request-ID", observability.RequestID(ctx))
	for _, key := range []string{"Content-Type", "Range", "If-Range", "X-Workspace-Socket"} {
		if value := input.Headers[key]; value != "" {
			req.Header.Set(key, value)
		}
	}
	// Обычный клиент ограничен пятью секундами. Поток ограничивает deadline запроса,
	// а остановку медленного consumer и отсутствие прогресса контролирует вызывающий.
	client := *c.http
	client.Timeout = 0
	if transport, ok := client.Transport.(*http.Transport); ok {
		copy := transport.Clone()
		copy.ResponseHeaderTimeout = 30 * time.Second
		copy.DisableKeepAlives = true
		client.Transport = copy
	}
	response, err := client.Do(req)
	if err != nil {
		return F.FileResponse{}, fmt.Errorf("workspace transport unavailable: %w", D.ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		_ = response.Body.Close()
		return F.FileResponse{}, D.ErrUnavailable
	}

	headers := make(map[string]string)
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Content-Range", "Accept-Ranges", "ETag", "Cache-Control", "X-Content-Type-Options"} {
		if value := response.Header.Get(key); value != "" {
			headers[key] = value
		}
	}
	return F.FileResponse{Status: response.StatusCode, Headers: headers, Body: response.Body}, nil
}
