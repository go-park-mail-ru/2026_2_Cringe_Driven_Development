package httptransport

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"go.uber.org/mock/gomock"
)

func TestNotebookReadOptionalBearerAndNoStore(t *testing.T) {
	for _, content := range []bool{false, true} {
		for _, tc := range []struct {
			name, auth        string
			user              int64
			allowed, validBFF bool
			status            int
		}{
			{"guest public", "", 0, true, true, 200},
			{"guest private", "", 0, false, true, 404},
			{"owner", "valid", 1, true, true, 200},
			{"reader", "valid", 1, true, true, 200},
			{"reader denied", "valid", 1, false, true, 404},
			{"invalid token", "Bearer invalid", 0, false, true, 401},
			{"missing service key", "", 0, false, false, 403},
		} {
			t.Run(tc.name+map[bool]string{true: " content", false: " metadata"}[content], func(t *testing.T) {
				f := newRouterFixture(t)
				path := BaseURL + "/notebooks/42"
				var err error
				if !tc.allowed {
					err = notebookusecase.ErrNotebookNotFound
				}
				reaches := tc.status == 200 || tc.status == 404
				if content {
					path += "/content"
					if reaches {
						f.notebooks.EXPECT().Content(gomock.Any(), int64(42), tc.user).Return(domain.Content{Hash: "rev", Cells: []domain.Cell{}}, err)
					}
				} else if reaches {
					ownerID := int64(2)
					if tc.name == "owner" {
						ownerID = 1
					}
					f.notebooks.EXPECT().Get(gomock.Any(), int64(42), tc.user).Return(domain.Notebook{ID: 42, Name: "shared", Access: domain.AccessFor(ownerID, tc.user)}, err)
				}
				req := httptest.NewRequest("GET", path, nil)
				if tc.validBFF {
					req.Header.Set("X-BFF-Key", "test-key")
				}
				if tc.auth == "valid" {
					req.Header.Set("Authorization", "Bearer "+f.access)
				} else if tc.auth != "" {
					req.Header.Set("Authorization", tc.auth)
				}
				rec := httptest.NewRecorder()
				f.handler.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body)
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("missing no-store")
				}
				if content && tc.status == 200 && rec.Header().Get("ETag") != `"rev"` {
					t.Fatal("ETag changed")
				}
				if !content && tc.status == 200 {
					var got api.Notebook
					if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Access.CanClone != (tc.user > 0) || got.Access.IsOwner != (tc.name == "owner") {
						t.Fatalf("wrong access: %+v", got.Access)
					}
					if strings.Contains(rec.Body.String(), "readers") {
						t.Fatal("reader list leaked")
					}
				}
			})
		}
	}
}

func TestNotebookSharingAndCloneHandlers(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		err                      error
		status                   int
	}{
		{"get sharing", "GET", "/sharing", "", nil, 200},
		{"get denied", "GET", "/sharing", "", notebookusecase.ErrNotebookNotFound, 404},
		{"put sharing", "PUT", "/sharing", `{"mode":"restricted","reader_logins":["reader"]}`, nil, 200},
		{"put unknown login", "PUT", "/sharing", `{"mode":"restricted","reader_logins":["reader"]}`, notebookusecase.ErrInvalidSharing, 400},
		{"put denied", "PUT", "/sharing", `{"mode":"restricted","reader_logins":["reader"]}`, notebookusecase.ErrNotebookNotFound, 404},
		{"clone", "POST", "/clone", `{"name":"copy"}`, nil, 201},
		{"clone denied", "POST", "/clone", `{"name":"copy"}`, notebookusecase.ErrNotebookNotFound, 404},
		{"clone invalid name", "POST", "/clone", `{"name":"copy"}`, notebookusecase.ErrInvalidName, 400},
		{"clone oversized", "POST", "/clone", `{"name":"copy"}`, notebookusecase.ErrTooLarge, 413},
		{"clone database error", "POST", "/clone", `{"name":"copy"}`, errors.New("private database failure"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouterFixture(t)
			settings := domain.SharingSettings{Mode: domain.SharingModeRestricted, Readers: []domain.NotebookReader{{ID: 2, Login: "reader"}}}
			switch tc.method {
			case "GET":
				f.notebooks.EXPECT().GetSharing(gomock.Any(), int64(42), int64(1)).Return(settings, tc.err)
			case "PUT":
				f.notebooks.EXPECT().PutSharing(gomock.Any(), int64(42), int64(1), domain.SharingModeRestricted, []string{"reader"}).Return(settings, tc.err)
			default:
				f.notebooks.EXPECT().Clone(gomock.Any(), int64(42), int64(1), "copy").Return(domain.Notebook{ID: 43, Name: "copy", Access: domain.AccessFor(1, 1)}, tc.err)
			}
			req := httptest.NewRequest(tc.method, BaseURL+"/notebooks/42"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+f.access)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body)
			}
			if tc.path == "/sharing" && rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			if strings.Contains(rec.Body.String(), "private database failure") {
				t.Fatal("database error leaked")
			}
		})
	}
}

func TestNotebookSharingAndCloneRequireSession(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/sharing", ""}, {"PUT", "/sharing", `{"mode":"private","reader_logins":[]}`}, {"POST", "/clone", `{"name":"copy"}`},
	} {
		f := newRouterFixture(t)
		req := httptest.NewRequest(tc.method, BaseURL+"/notebooks/42"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-BFF-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
}

// Expiry is checked by the real token parser before optional guest fallback.
func TestNotebookExpiredBearerCannotBecomeGuest(t *testing.T) {
	tokens := usersecurity.NewAccessToken([]byte("expiry-test"), -time.Minute)
	token, err := tokens.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRouter(Server{NotebookHandler: notebookdelivery.NewHandler(nil)}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", BaseURL+"/notebooks/42", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-BFF-Key", "test-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestSuggestUsersHTTPAcceptsWhitespaceBeforeNormalization(t *testing.T) {
	for _, q := range []string{" reader ", " " + strings.Repeat("a", 32) + " "} {
		t.Run(q, func(t *testing.T) {
			f := newRouterFixture(t)
			f.users.EXPECT().SuggestUsers(gomock.Any(), int64(1), q, 10).Return(nil, nil)
			req := httptest.NewRequest("GET", BaseURL+"/users/suggest?q="+url.QueryEscape(q), nil)
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+f.access)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCloneNameTrimmedBeforeLengthValidation(t *testing.T) {
	f := newRenameHTTPFixture(t)
	name := strings.Repeat("я", 128)
	f.repo.EXPECT().Clone(gomock.Any(), int64(42), int64(1), name).Return(domain.Notebook{ID: 43, OwnerID: 1, Name: name}, nil)
	body, err := json.Marshal(map[string]string{"name": " " + name + " "})
	if err != nil {
		t.Fatal(err)
	}
	req := f.request("42/clone", string(body))
	req.Method = "POST"
	rec := serveRouter(f.handler, req)
	if rec.Code != 201 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}
