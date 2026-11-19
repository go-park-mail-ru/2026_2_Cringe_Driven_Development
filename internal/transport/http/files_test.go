package httptransport

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	delivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	security "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"github.com/google/uuid"
)

// filesRuntime проверяет допуск запроса к файловому сценарию через общий router.
type filesRuntime struct {
	delivery.Usecase
	called bool
}

// Files возвращает безопасный ответ только после передачи проверенного владельца.
func (s *filesRuntime) Files(_ context.Context, owner int64, _ uuid.UUID, input F.FileRequest) (F.FileResponse, error) {
	if owner != 7 {
		return F.FileResponse{}, D.ErrNotFound
	}
	s.called = true
	return F.FileResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"result":{}}`))}, nil
}

// TestWorkspaceRoutesRequireS2SAndCurrentPrincipal проверяет штатный HTTP-вход без прямого обращения к агенту.
func TestWorkspaceRoutesRequireS2SAndCurrentPrincipal(t *testing.T) {
	issuer := security.NewAccessToken([]byte("files-router-test-key"), time.Hour)
	token, err := issuer.Issue(testClaims(7))
	if err != nil {
		t.Fatal(err)
	}
	uc := &filesRuntime{}
	handler, err := NewRouter(Server{RuntimeHandler: delivery.NewHandler(uc)}, issuer, allowSessions{}, "file-bff-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, token string
		status     int
	}{{"", token, 403}, {"file-bff-key", "", 401}, {"file-bff-key", "bad", 401}, {"file-bff-key", token, 200}} {
		uc.called = false
		request := httptest.NewRequest("POST", BaseURL+"/runtime-sessions/"+uuid.NewString()+"/workspace/command", strings.NewReader(`{}`))
		request.Header.Set("X-BFF-Key", tc.key)
		request.Header.Set("Authorization", "Bearer "+tc.token)
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, request)
		if writer.Code != tc.status || uc.called != (tc.status == 200) {
			t.Fatalf("status=%d called=%v body=%s", writer.Code, uc.called, writer.Body.String())
		}
	}
}
