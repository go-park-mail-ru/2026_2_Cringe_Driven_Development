package app

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
)

func TestCSRFRestoreExistingAccessSession(t *testing.T) {
	for _, existing := range []bool{false, true} {
		handler, csrf, _, access, _ := csrfRouter(t)
		token := ""
		if existing {
			token = csrf.Anonymous()
		}
		rec := serveCSRF(handler, csrfRequest("GET", baseURL+"/users/me", token, access, ""))
		if rec.Code != 200 {
			t.Fatalf("session check: %d %s", rec.Code, rec.Body)
		}
		cookie := responseCSRF(t, rec)
		if cookie == nil || !csrf.Valid(cookie.Value, 1) {
			t.Fatal("existing access session received no signed CSRF")
		}
		// Подписанную cookie не переиздаём на следующих ответах.
		next := serveCSRF(handler, csrfRequest("GET", baseURL+"/users/me", cookie.Value, access, ""))
		if next.Code != 200 || responseCSRF(t, next) != nil {
			t.Fatal("signed CSRF was not stable")
		}
		next = serveCSRF(handler, csrfRequest("POST", baseURL+"/notebooks", cookie.Value, access, ""))
		if next.Code != 201 {
			t.Fatalf("restored session cannot mutate: %d %s", next.Code, next.Body)
		}
	}
}

func TestCSRFRestoreExpiredAccessSession(t *testing.T) {
	for _, operation := range []string{"refresh", "logout"} {
		for _, existing := range []bool{false, true} {
			t.Run(operation+"/existing="+strconv.FormatBool(existing), func(t *testing.T) {
				handler, csrf, users, _, expired := csrfRouter(t)
				token := ""
				if existing {
					token = csrf.Anonymous()
				}
				path := baseURL + "/auth/" + operation
				rec := serveCSRF(handler, csrfRequest("POST", path, token, expired, "refresh"))
				assertCSRFError(t, rec)
				cookie := responseCSRF(t, rec)
				if cookie == nil || !csrf.Valid(cookie.Value, 1) {
					t.Fatal("expired access session received no signed CSRF")
				}
				if users.mutations != 0 {
					t.Fatal("bootstrap bypassed CSRF and changed session")
				}
				rec = serveCSRF(handler, csrfRequest("POST", path, cookie.Value, expired, "refresh"))
				if rec.Code != http.StatusNoContent || users.mutations != 1 {
					t.Fatalf("retry failed: %d %s", rec.Code, rec.Body)
				}
			})
		}
	}
}

func TestCSRFRestoreAfterGuestBootstrap(t *testing.T) {
	handler, csrf, users, _, expired := csrfRouter(t)
	// В реальном браузере refresh-cookie не отправляется на /users/me из-за Path.
	rec := serveCSRF(handler, csrfRequest("GET", baseURL+"/users/me", "", expired, ""))
	cookie := responseCSRF(t, rec)
	if rec.Code != 401 || cookie == nil || !auth.IsAnonymousCSRF(cookie.Value) {
		t.Fatal("expected guest bootstrap without refresh cookie")
	}
	rec = serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/refresh", cookie.Value, expired, "refresh"))
	assertCSRFError(t, rec)
	cookie = responseCSRF(t, rec)
	if cookie == nil || !csrf.Valid(cookie.Value, 1) || users.mutations != 0 {
		t.Fatal("refresh did not upgrade anonymous CSRF safely")
	}
	rec = serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/refresh", cookie.Value, expired, "refresh"))
	if rec.Code != 204 {
		t.Fatalf("refresh retry failed: %d %s", rec.Code, rec.Body)
	}
}

func TestCSRFBootstrapDoesNotRepairInvalidSignature(t *testing.T) {
	handler, csrf, users, _, expired := csrfRouter(t)
	for _, token := range []string{csrf.Signed(2), "broken.signature", "invalid-anonymous"} {
		rec := serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/refresh", token, expired, "refresh"))
		assertCSRFError(t, rec)
		if responseCSRF(t, rec) != nil || users.mutations != 0 {
			t.Fatal("invalid token was repaired or session mutated")
		}
	}
}

func TestCSRFBootstrapRejectsForeignOrigin(t *testing.T) {
	handler, csrf, users, access, expired := csrfRouter(t)
	for _, token := range []string{"", csrf.Anonymous()} {
		for _, accessToken := range []string{access, expired} {
			req := csrfRequest("POST", baseURL+"/auth/refresh", token, accessToken, "refresh")
			req.Header.Set("Origin", "https://evil.example")
			rec := serveCSRF(handler, req)
			assertCSRFError(t, rec)
			cookie := responseCSRF(t, rec)
			if cookie != nil && !auth.IsAnonymousCSRF(cookie.Value) {
				t.Fatal("foreign origin received signed CSRF")
			}
			if users.mutations != 0 {
				t.Fatal("foreign origin changed session")
			}
		}
	}
}

func TestCSRFBootstrapIgnoresInvalidRefresh(t *testing.T) {
	handler, csrf, users, _, expired := csrfRouter(t)
	for _, token := range []string{"", csrf.Anonymous()} {
		rec := serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/refresh", token, expired, "unknown-refresh"))
		if token == "" {
			assertCSRFError(t, rec)
		} else if rec.Code != 401 {
			t.Fatalf("invalid refresh: %d %s", rec.Code, rec.Body)
		}
		cookie := responseCSRF(t, rec)
		if cookie != nil && !auth.IsAnonymousCSRF(cookie.Value) {
			t.Fatal("invalid refresh received signed CSRF")
		}
		if users.mutations != 0 {
			t.Fatal("invalid refresh changed session")
		}
	}
}

func TestCSRFBootstrapKeepsSessionRotationCookie(t *testing.T) {
	handler, csrf, _, access, _ := csrfRouter(t)
	rec := serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/login", csrf.Anonymous(), access, ""))
	cookie := responseCSRF(t, rec)
	if rec.Code != 200 || cookie == nil || !csrf.Valid(cookie.Value, 1) {
		t.Fatal("session rotation did not take precedence over bootstrap")
	}
}

func TestCSRFBootstrapOnReadWithOnlyRefresh(t *testing.T) {
	handler, csrf, users, _, _ := csrfRouter(t)
	for _, token := range []string{"", csrf.Anonymous()} {
		// Этот путь получает refresh-cookie; выдача CSRF работает и на ответе 405.
		rec := serveCSRF(handler, csrfRequest("GET", baseURL+"/auth/refresh", token, "", "refresh"))
		cookie := responseCSRF(t, rec)
		if rec.Code != 405 || cookie == nil || !csrf.Valid(cookie.Value, 1) || users.mutations != 0 {
			t.Fatal("read-only bootstrap with refresh failed")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("bootstrap response can be cached")
		}
	}
}

func TestCSRFBootstrapRepositoryFailure(t *testing.T) {
	handler, csrf, users, _, expired := csrfRouter(t)
	users.lookupErr = errors.New("session storage unavailable")
	rec := serveCSRF(handler, csrfRequest("POST", baseURL+"/auth/refresh", csrf.Anonymous(), expired, "refresh"))
	if rec.Code != 500 || users.mutations != 0 || responseCSRF(t, rec) != nil {
		t.Fatal("storage failure was treated as valid session")
	}
	users.lookupErr = nil
	rec = serveCSRF(handler, csrfRequest("GET", baseURL+"/auth/refresh", csrf.Anonymous(), expired, "refresh"))
	cookie := responseCSRF(t, rec)
	if cookie == nil || !csrf.Valid(cookie.Value, 1) {
		t.Fatal("bootstrap cannot recover after storage failure")
	}
}
