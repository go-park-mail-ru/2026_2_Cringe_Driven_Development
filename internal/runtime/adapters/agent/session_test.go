package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// TestAgentCommandPreservesTimeoutAndID проверяет передачу ID, исходника, таймаута, Bearer и клиентского
// TLS-сертификата агенту.
func TestAgentCommandPreservesTimeoutAndID(t *testing.T) {
	ca := newCA(t, "ca")
	srv, seen := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"execution_id":"run","state":"QUEUED"}`))
	})
	c := newClient(t, ca)
	x, err := c.ExecuteCommand(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, D.Command{ExecutionID: "run", Code: "print('<')", TimeoutSeconds: 17})
	if err != nil || x.ExecutionID != "run" || x.State != "QUEUED" {
		t.Fatal(x, err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	var body D.Command
	if err = json.Unmarshal([]byte(seen.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.TimeoutSeconds != 17 || body.Code != "print('<')" || seen.auth != "Bearer "+tok || seen.peers != 1 {
		t.Fatal(body, seen.auth, seen.peers)
	}
}

// TestLargeEventNotTruncated проверяет, что большой первый элемент страницы событий не обрезается при
// чтении.
func TestLargeEventNotTruncated(t *testing.T) {
	ca := newCA(t, "ca")
	text := strings.Repeat("x", 5<<20)
	srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{map[string]any{"seq": 1, "kind": "kernel_message", "content": map[string]string{"text": text}}}})
	})
	events, err := newClient(t, ca).ReadEvents(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, 0, D.EventLimits{})
	if err != nil || len(events) != 1 || len(events[0].Content) < len(text) {
		t.Fatal(len(events), err)
	}
}

// TestAgentStatusClassification проверяет перевод HTTP-отказов агента в ожидаемые ошибки runtime.
func TestAgentStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{400, D.ErrInvalid}, {409, D.ErrConflict}, {429, D.ErrQueueFull}, {503, D.ErrUnavailable}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			ca := newCA(t, "ca")
			srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) })
			_, err := newClient(t, ca).ExecuteCommand(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, D.Command{ExecutionID: "run", Code: "x", TimeoutSeconds: 300})
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

// TestSessionClientRejectsRedirect проверяет отказ от перенаправления запроса с секретным токеном на другой
// адрес.
func TestSessionClientRejectsRedirect(t *testing.T) {
	ca := newCA(t, "ca")
	calls := 0
	srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	_, err := newClient(t, ca).ReadEvents(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, 0, D.EventLimits{})
	if err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

// TestInspectionUsesDedicatedAgentRouteAndBody проверяет отправку инспекции в /variables без полей обычной
// кодовой команды.
func TestInspectionUsesDedicatedAgentRouteAndBody(t *testing.T) {
	ca := newCA(t, "ca")
	srv, seen := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"execution_id":"inspect-1","state":"QUEUED"}`))
	})
	_, err := newClient(t, ca).ExecuteCommand(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, D.Command{ExecutionID: "inspect-1", Kind: "inspect", TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	var body map[string]any
	if err = json.Unmarshal([]byte(seen.body), &body); err != nil {
		t.Fatal(err)
	}
	if seen.path != "/variables" || len(body) != 1 || body["execution_id"] != "inspect-1" {
		t.Fatalf("unexpected inspection request: %s %v", seen.path, body)
	}
}

// TestKernelRestartReturnsGenerationAndDoesNotRetryFailure проверяет чтение поколения kernel и отсутствие
// автоматического повтора restart при отказе.
func TestKernelRestartReturnsGenerationAndDoesNotRetryFailure(t *testing.T) {
	for _, status := range []int{200, 409, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ca := newCA(t, "ca")
			calls := 0
			srv, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/kernel/restart" {
					t.Error(r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"generation":"d8838602-1de6-4f40-ad6a-a1a558ef682e"}`))
			})
			generation, err := newClient(t, ca).Restart(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok})
			if status == 200 {
				if err != nil || generation == "" {
					t.Fatal(generation, err)
				}
			} else if err == nil {
				t.Fatal("failure accepted")
			}
			if calls != 1 {
				t.Fatal("unsafe retry", calls)
			}
		})
	}
}

func TestStopExecutionsRequiresConfirmedResponse(t *testing.T) {
	for _, body := range []string{`{"stopped":true}`, `{"stopped":false}`, `{"interrupt_requested":true}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			ca := newCA(t, "ca")
			srv, seen := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/executions/stop" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(body))
			})
			err := newClient(t, ca).StopExecutions(context.Background(), usecase.AgentEndpoint{BaseURL: srv.URL, Token: tok}, "operation", []string{"run"})
			if (err == nil) != (body == `{"stopped":true}`) {
				t.Fatal(err)
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			var request struct {
				OperationID  string   `json:"operation_id"`
				ExecutionIDs []string `json:"execution_ids"`
			}
			if e := json.Unmarshal([]byte(seen.body), &request); e != nil || request.OperationID != "operation" || len(request.ExecutionIDs) != 1 || request.ExecutionIDs[0] != "run" {
				t.Fatal(request, e)
			}
		})
	}
}
