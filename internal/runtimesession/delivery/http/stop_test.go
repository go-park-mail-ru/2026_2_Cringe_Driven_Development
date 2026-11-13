package delivery

import (
	"context"
	"net/http/httptest"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/google/uuid"
)

type stopUsecase struct {
	Usecase
	err   error
	owner int64
	id    uuid.UUID
}

func (s *stopUsecase) StopExecutions(_ context.Context, owner int64, id uuid.UUID) error {
	s.owner = owner
	s.id = id
	return s.err
}
func TestStopExecutionsResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"ok", nil, 200}, {"missing", D.ErrNotFound, 404}, {"conflict", D.ErrConflict, 409}, {"unavailable", D.ErrUnavailable, 503}} {
		t.Run(tc.name, func(t *testing.T) {
			uc := &stopUsecase{err: tc.err}
			id := uuid.New()
			h := NewHandler(uc)
			resp, err := h.StopRuntimeExecutions(principal.WithUserID(context.Background(), 7), api.StopRuntimeExecutionsRequestObject{Id: id})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			if err = resp.VisitStopRuntimeExecutionsResponse(w); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || uc.owner != 7 || uc.id != id {
				t.Fatalf("status=%d owner=%d id=%s", w.Code, uc.owner, uc.id)
			}
			if tc.status == 200 && w.Body.String() != "{\"stopped\":true}\n" {
				t.Fatal(w.Body.String())
			}
		})
	}
}
