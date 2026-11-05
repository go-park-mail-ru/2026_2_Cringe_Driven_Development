package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/google/uuid"
)

// testUsecase подставляет результаты сценариев и запоминает владельца в HTTP-тестах.
type testUsecase struct {
	Usecase
	// active Активная сессия, подставляемая в тесте.
	active *D.Session
	// err Ошибка, подставляемая в тесте.
	err error
	// owner ID владельца, полученный тестовым сценарием.
	owner int64
}

// GetActive подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *testUsecase) GetActive(_ context.Context, owner int64) (*D.Session, error) {
	s.owner = owner
	return s.active, s.err
}

// Start подставляет результат операции и сохраняет переданные аргументы для проверки HTTP-контракта.
func (s *testUsecase) Start(_ context.Context, owner, notebook int64, r D.StartRequest) (D.Session, error) {
	s.owner = owner
	return D.Session{}, s.err
}

// TestRuntimeActiveNullAndOwnerIsolation проверяет явное отсутствие активной сессии и передачу проверенного
// владельца в сценарий.
func TestRuntimeActiveNullAndOwnerIsolation(t *testing.T) {
	uc := &testUsecase{}
	h := NewHandler(uc)
	resp, err := h.GetActiveRuntimeSession(principal.WithUserID(context.Background(), 7), api.GetActiveRuntimeSessionRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err = resp.VisitGetActiveRuntimeSessionResponse(w); err != nil {
		t.Fatal(err)
	}
	if uc.owner != 7 || strings.TrimSpace(w.Body.String()) != `{"active_session":null}` {
		t.Fatal(uc.owner, w.Body.String())
	}
}

// TestRuntimeStartConflictPayload проверяет ответ 409 с данными уже активной сессии.
func TestRuntimeStartConflictPayload(t *testing.T) {
	id := uuid.New()
	uc := &testUsecase{err: &D.ActiveSessionError{Session: D.Session{ID: id, NotebookID: 99, State: D.Ready, Language: "python", Stage: "ready"}}}
	h := NewHandler(uc)
	resp, err := h.StartRuntimeSession(principal.WithUserID(context.Background(), 7), api.StartRuntimeSessionRequestObject{Id: 4, Body: &api.RuntimeStartRequest{RequestId: "request", Language: "python"}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err = resp.VisitStartRuntimeSessionResponse(w); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Code   string `json:"code"`
		Active struct {
			ID       uuid.UUID `json:"id"`
			Notebook int64     `json:"notebook_id"`
		} `json:"active_session"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 409 || body.Code != "active_session_exists" || body.Active.ID != id || body.Active.Notebook != 99 {
		t.Fatal(w.Code, w.Body.String())
	}
	uc.err = errors.New("secret infrastructure error")
	_, err = h.GetActiveRuntimeSession(principal.WithUserID(context.Background(), 7), api.GetActiveRuntimeSessionRequestObject{})
	if err == nil {
		t.Fatal("DB error masked")
	}
}
