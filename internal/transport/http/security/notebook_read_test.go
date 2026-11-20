package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	userusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"go.uber.org/mock/gomock"
)

type notebookReadSessions struct{ err error }

func (s notebookReadSessions) ValidateAccess(_ context.Context, _ userusecase.SessionClaims) error {
	return s.err
}

func TestNotebookReadRejectsSuppliedInvalidAuthorization(t *testing.T) {
	for _, path := range []string{"/api/v1/notebooks/42", "/api/v1/notebooks/42/content"} {
		for _, tc := range []struct {
			name                     string
			headers                  []string
			parse                    bool
			tokenError, sessionError error
			status                   int
		}{
			{name: "guest", status: 204},
			{name: "malformed", headers: []string{"Basic xxx"}, status: 401},
			{name: "empty", headers: []string{""}, status: 401},
			{name: "duplicate", headers: []string{"Bearer token", "Bearer token"}, status: 401},
			{name: "invalid or expired", headers: []string{"Bearer token"}, parse: true, tokenError: errors.New("invalid token"), status: 401},
			{name: "revoked", headers: []string{"Bearer token"}, parse: true, sessionError: userusecase.ErrInvalidSession, status: 401},
			{name: "valid", headers: []string{"Bearer token"}, parse: true, status: 204},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				parser := NewMockTokenParser(gomock.NewController(t))
				if tc.parse {
					parser.EXPECT().Parse("token").Return(testClaims(7), tc.tokenError)
				}
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, ok := principal.UserIDFromContext(r.Context())
					if ok != (tc.name == "valid") {
						t.Fatalf("unexpected principal: %v", ok)
					}
					w.WriteHeader(http.StatusNoContent)
				})
				req := httptest.NewRequest(http.MethodGet, path, nil)
				for _, h := range tc.headers {
					req.Header.Add("Authorization", h)
				}
				rec := httptest.NewRecorder()
				AuthenticateBearer(parser, notebookReadSessions{err: tc.sessionError})(next).ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status=%d want=%d", rec.Code, tc.status)
				}
			})
		}
	}
}
