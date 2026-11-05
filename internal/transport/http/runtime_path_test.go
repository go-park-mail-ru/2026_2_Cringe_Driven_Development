package httptransport

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	delivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	security "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"github.com/google/uuid"
)

// pathRuntime запоминает execution_id после прохождения маршрутизатора для проверки декодирования пути.
type pathRuntime struct {
	delivery.Usecase
	// got Аргумент, полученный тестовой зависимостью.
	got string
}

// GetExecution подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *pathRuntime) GetExecution(_ context.Context, _ int64, id uuid.UUID, execution string) (D.Execution, error) {
	s.got = execution
	return D.Execution{SessionID: id, ID: execution, CellID: "cell", State: "SUCCEEDED", Projection: "applied", TimeoutSeconds: 300, Attempt: 1}, nil
}

// TestRuntimeExecutionEscapedID проверяет сохранность execution_id со слешами, знаком плюс и Unicode после
// декодирования маршрута.
func TestRuntimeExecutionEscapedID(t *testing.T) {
	issuer := security.NewAccessToken([]byte("runtime-path-key"), time.Hour)
	token, err := issuer.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	uc := &pathRuntime{}
	handler, err := NewRouter(Server{RuntimeHandler: delivery.NewHandler(uc)}, issuer, allowSessions{}, "path-bff-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run/1", "a%2Fb", "a+b", "ячейка"} {
		t.Run(id, func(t *testing.T) {
			uc.got = ""
			r := httptest.NewRequest("GET", BaseURL+"/runtime-sessions/"+uuid.NewString()+"/executions/"+url.PathEscape(id), nil)
			r.Header.Set("X-BFF-Key", "path-bff-key")
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 || uc.got != id {
				t.Fatalf("id %q: status %d received %q body %s", id, w.Code, uc.got, w.Body.String())
			}
		})
	}
}
