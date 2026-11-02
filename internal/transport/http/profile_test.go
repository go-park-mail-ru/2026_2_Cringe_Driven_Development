package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

type profileUsers struct {
	usecase.Usecase
	failure                  error
	id                       int64
	login, current, password string
}

func (u *profileUsers) CurrentUser(context.Context, int64) (domain.User, error) {
	return domain.User{ID: 42, Login: "alice", AvatarKey: "avatars/picture.png"}, nil
}
func (u *profileUsers) UpdateCurrentUser(_ context.Context, id int64, login string) (domain.User, error) {
	u.id = id
	u.login = login
	return domain.User{ID: id, Login: login}, u.failure
}
func (u *profileUsers) ChangeCurrentUserPassword(_ context.Context, id int64, current, password string) error {
	u.id = id
	u.current = current
	u.password = password
	return u.failure
}

type profileAvatars struct {
	failure  error
	calls    int
	contents string
	id       int64
}

func (u *profileAvatars) Upload(_ context.Context, id int64, source io.ReadSeeker, _ string) (domain.User, error) {
	u.id = id
	u.calls++
	var b strings.Builder
	_, _ = io.Copy(&b, source)
	u.contents = b.String()
	return domain.User{ID: id, Login: "alice", AvatarKey: "avatars/new.png"}, u.failure
}
func profileRouter(t *testing.T, users *profileUsers, avatars *profileAvatars) (http.Handler, string) {
	t.Helper()
	tokens := usersecurity.NewAccessToken([]byte("profile-test"), time.Minute)
	access, err := tokens.Issue(testClaims(42))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(users, userdelivery.WithProfile(avatars, "https://avatars.example.com"))}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return h, access
}
func TestProfileJSONOperations(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		failure                  error
		status                   int
		code                     string
	}{
		{name: "current", method: "GET", path: "/users/me", status: 200},
		{name: "login", method: "PATCH", path: "/users/me", body: `{"login":"alice_new"}`, status: 200},
		{name: "taken", method: "PATCH", path: "/users/me", body: `{"login":"alice_new"}`, failure: usecase.ErrLoginTaken, status: 409, code: "login_taken"},
		{name: "unknown field", method: "PATCH", path: "/users/me", body: `{"login":"alice_new","id":1}`, status: 400, code: "validation_error"},
		{name: "password", method: "PUT", path: "/users/me/password", body: `{"current_password":"password123","new_password":"new-password"}`, status: 204},
		{name: "wrong password", method: "PUT", path: "/users/me/password", body: `{"current_password":"password123","new_password":"new-password"}`, failure: usecase.ErrInvalidCurrentPassword, status: 400, code: "invalid_current_password"},
		{name: "missing password", method: "PUT", path: "/users/me/password", body: `{}`, status: 400, code: "validation_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := &profileUsers{failure: tc.failure}
			h, access := profileRouter(t, users, &profileAvatars{})
			req := httptest.NewRequest(tc.method, BaseURL+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+access)
			res := serveRouter(h, req)
			if res.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", res.Code, tc.status, res.Body)
			}
			if tc.code != "" {
				var e struct{ Code string }
				_ = json.Unmarshal(res.Body.Bytes(), &e)
				if e.Code != tc.code {
					t.Fatalf("code=%s body=%s", e.Code, res.Body)
				}
			}
			if tc.name == "current" && !strings.Contains(res.Body.String(), "https://avatars.example.com/avatars/picture.png") {
				t.Fatal(res.Body)
			}
			if tc.name == "login" && users.id != 42 {
				t.Fatalf("wrong user %d", users.id)
			}
			if tc.status == 204 && res.Body.Len() != 0 {
				t.Fatal("204 has body")
			}
		})
	}
}
func avatarMultipart(t *testing.T, parts int, data string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for i := 0; i < parts; i++ {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="avatar"; filename="photo.png"`)
		header.Set("Content-Type", "image/png")
		p, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(p, data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), w.FormDataContentType()
}
func TestProfileAvatarMultipart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parts   int
		data    string
		failure error
		status  int
		code    string
	}{
		{name: "upload", parts: 1, data: "photo", status: 200},
		{name: "empty multipart", parts: 0, status: 400, code: "validation_error"},
		{name: "duplicate", parts: 2, data: "photo", status: 400, code: "validation_error"},
		{name: "too large", parts: 1, data: strings.Repeat("x", 5*1024*1024+1), status: 413, code: "payload_too_large"},
		{name: "invalid pixels", parts: 1, data: "photo", failure: usecase.ErrInvalidImage, status: 400, code: "validation_error"},
		{name: "mismatch", parts: 1, data: "photo", failure: usecase.ErrUnsupportedMediaType, status: 415, code: "unsupported_media_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			avatars := &profileAvatars{failure: tc.failure}
			h, access := profileRouter(t, &profileUsers{}, avatars)
			body, ct := avatarMultipart(t, tc.parts, tc.data)
			req := httptest.NewRequest("PUT", BaseURL+"/users/me/avatar", bytes.NewReader(body))
			req.Header.Set("Content-Type", ct)
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+access)
			res := serveRouter(h, req)
			if res.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", res.Code, tc.status, res.Body)
			}
			if tc.status == 200 && (avatars.calls != 1 || avatars.contents != "photo" || avatars.id != 42) {
				t.Fatalf("avatar uploader %+v", avatars)
			}
			if tc.name == "duplicate" && avatars.calls != 0 {
				t.Fatal("published before entire multipart validated")
			}
		})
	}
}

type unreadBody struct{ reads int }

func (b *unreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body should not be read")
}
func (b *unreadBody) Close() error { return nil }
func TestAvatarAuthorizationBeforeBody(t *testing.T) {
	for _, tc := range []struct {
		key, bearer string
		status      int
	}{{status: 403}, {key: "test-key", status: 401}} {
		h, _ := profileRouter(t, &profileUsers{}, &profileAvatars{})
		body := &unreadBody{}
		req := httptest.NewRequest("PUT", BaseURL+"/users/me/avatar", nil)
		req.Body = body
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-BFF-Key", tc.key)
		res := serveRouter(h, req)
		if res.Code != tc.status || body.reads != 0 {
			t.Fatalf("status=%d reads=%d", res.Code, body.reads)
		}
	}
}

func TestAvatarMalformedAndTotalLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		length                  int64
		status                  int
	}{
		{name: "malformed headers", body: "--photo\r\ninvalidheader\r\n\r\nx\r\n--photo--\r\n", contentType: "multipart/form-data; boundary=photo", length: -1, status: 400},
		{name: "unexpected request type", body: "{}", contentType: "application/json", length: -1, status: 415},
		{name: "declared too large", body: "", contentType: "multipart/form-data; boundary=photo", length: 6*1024*1024 + 1, status: 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &profileAvatars{}
			h, access := profileRouter(t, &profileUsers{}, a)
			req := httptest.NewRequest("PUT", BaseURL+"/users/me/avatar", strings.NewReader(tc.body))
			req.ContentLength = tc.length
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+access)
			res := serveRouter(h, req)
			if res.Code != tc.status || a.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", res.Code, tc.status, a.calls, res.Body)
			}
		})
	}
	t.Run("chunked epilogue exceeds total limit", func(t *testing.T) {
		a := &profileAvatars{}
		h, access := profileRouter(t, &profileUsers{}, a)
		body, ct := avatarMultipart(t, 1, "photo")
		body = append(body, bytes.Repeat([]byte("x"), 6*1024*1024)...)
		req := httptest.NewRequest("PUT", BaseURL+"/users/me/avatar", bytes.NewReader(body))
		req.ContentLength = -1
		req.Header.Set("Content-Type", ct)
		req.Header.Set("X-BFF-Key", "test-key")
		req.Header.Set("Authorization", "Bearer "+access)
		res := serveRouter(h, req)
		if res.Code != 413 || a.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", res.Code, a.calls, res.Body)
		}
	})
}

type boundedReadBody struct {
	io.Reader
	largest int
}

func (b *boundedReadBody) Read(p []byte) (int, error) {
	if len(p) > b.largest {
		b.largest = len(p)
	}
	return b.Reader.Read(p)
}
func (b *boundedReadBody) Close() error { return nil }
func TestAvatarStreamsBodyWithoutSecurityBuffering(t *testing.T) {
	a := &profileAvatars{}
	h, access := profileRouter(t, &profileUsers{}, a)
	data, ct := avatarMultipart(t, 1, strings.Repeat("x", 1024*1024))
	body := &boundedReadBody{Reader: bytes.NewReader(data)}
	req := httptest.NewRequest("PUT", BaseURL+"/users/me/avatar", nil)
	req.Body = body
	req.ContentLength = -1
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-BFF-Key", "test-key")
	req.Header.Set("Authorization", "Bearer "+access)
	res := serveRouter(h, req)
	if res.Code != 200 || body.largest > 32*1024 {
		t.Fatalf("status=%d largest buffer read=%d body=%s", res.Code, body.largest, res.Body)
	}
}
