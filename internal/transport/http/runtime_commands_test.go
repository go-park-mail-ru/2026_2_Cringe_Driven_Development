package httptransport

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	delivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	security "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"github.com/google/uuid"
)

// commandRuntime запоминает вызовы новых runtime-операций для проверки авторизации и HTTP-контракта.
type commandRuntime struct {
	delivery.Usecase
	// calls Число вызовов зависимости в тесте.
	calls int
	// owner ID владельца, полученный тестовым сценарием.
	owner int64
	// batch Результат массового запуска для проверки HTTP-ответа.
	batch D.BatchRequest
}

// Queue подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *commandRuntime) Queue(_ context.Context, owner int64, id uuid.UUID) (D.Queue, error) {
	s.calls++
	s.owner = owner
	return D.Queue{SessionID: id, Items: []D.QueueItem{}}, nil
}

// ExecuteBatch подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *commandRuntime) ExecuteBatch(_ context.Context, owner int64, id uuid.UUID, r D.BatchRequest) (D.Batch, error) {
	s.calls++
	s.owner = owner
	s.batch = r
	return D.Batch{SessionID: id, RequestID: r.RequestID, ExecutionIDs: []string{"run-1"}}, nil
}

// Inspect подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *commandRuntime) Inspect(_ context.Context, owner int64, id uuid.UUID, r D.InspectRequest) (D.Inspection, error) {
	s.calls++
	s.owner = owner
	return D.Inspection{SessionID: id, ExecutionID: r.ExecutionID, State: "QUEUED", Variables: []D.Variable{}}, nil
}

// GetInspection подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *commandRuntime) GetInspection(_ context.Context, owner int64, id uuid.UUID, execution string) (D.Inspection, error) {
	return s.Inspect(context.Background(), owner, id, D.InspectRequest{ExecutionID: execution})
}

// Kernel подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *commandRuntime) Kernel(_ context.Context, owner int64, _ uuid.UUID, _ bool) (string, error) {
	s.calls++
	s.owner = owner
	return uuid.NewString(), nil
}

func (s *commandRuntime) StopExecutions(_ context.Context, owner int64, _ uuid.UUID) error {
	s.calls++
	s.owner = owner
	return nil
}

// TestNewRuntimeCommandsRequireAuthAndPreserveWireContract проверяет авторизацию новых ручек до побочных
// действий и соответствие их ответов контракту.
func TestNewRuntimeCommandsRequireAuthAndPreserveWireContract(t *testing.T) {
	issuer := security.NewAccessToken([]byte("commands-key"), time.Hour)
	token, err := issuer.Issue(testClaims(7))
	if err != nil {
		t.Fatal(err)
	}
	uc := &commandRuntime{}
	handler, err := NewRouter(Server{RuntimeHandler: delivery.NewHandler(uc)}, issuer, allowSessions{}, "commands-bff-key")
	if err != nil {
		t.Fatal(err)
	}
	base := BaseURL + "/runtime-sessions/" + uuid.NewString()
	for _, tc := range []struct {
		method, path, body, contains string
		status                       int
	}{
		{"GET", "/queue", "", `"items":[]`, 200},
		{"POST", "/executions/batch", `{"request_id":"batch-1","cells":[{"cell_id":"a","source_hash":"` + strings.Repeat("a", 64) + `"}]}`, `"execution_ids":["run-1"]`, 202},
		{"POST", "/variables", `{"execution_id":"inspect-1"}`, `"variables":[]`, 202},
		{"GET", "/variables/inspect-1", "", `"execution_id":"inspect-1"`, 200},
		{"POST", "/executions/stop", "", `"stopped":true`, 200},
		{"POST", "/kernel/interrupt", "", `"interrupt_requested":true`, 200},
		{"POST", "/kernel/restart", "", `"generation":`, 200},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			for _, auth := range []bool{false, true} {
				before := uc.calls
				r := httptest.NewRequest(tc.method, base+tc.path, strings.NewReader(tc.body))
				if tc.body != "" {
					r.Header.Set("Content-Type", "application/json")
				}
				r.Header.Set("Authorization", "Bearer "+token)
				if auth {
					r.Header.Set("X-BFF-Key", "commands-bff-key")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if !auth {
					if w.Code != 403 || uc.calls != before {
						t.Fatalf("S2S rejection had side effects: %d %d", w.Code, uc.calls-before)
					}
					continue
				}
				if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) || uc.owner != 7 {
					t.Fatalf("%d %s owner=%d", w.Code, w.Body.String(), uc.owner)
				}
			}
		})
	}
	if uc.batch.TimeoutSeconds != 300 || uc.batch.Cells[0].CellID != "a" {
		t.Fatal(uc.batch)
	}
}
