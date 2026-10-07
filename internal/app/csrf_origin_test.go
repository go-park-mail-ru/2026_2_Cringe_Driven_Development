package app

import (
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
)

func TestCSRFAppOriginWithoutCORS(t *testing.T) {
	for _, appOrigin := range []string{middleware.DefaultAppOrigin, "http://localhost:8080"} {
		for _, operation := range []string{"login", "register", "refresh"} {
			for _, origin := range []string{appOrigin, "https://evil.example", "https://avatars.cellestial.ru", "null"} {
				t.Run(appOrigin+"/"+operation+"/"+origin, func(t *testing.T) {
					handler, csrf, _, _, _ := csrfRouterOrigins(t, nil, appOrigin)
					token := csrf.Anonymous()
					if operation == "refresh" {
						token = csrf.Signed(1)
					}
					req := csrfRequest("POST", baseURL+"/auth/"+operation, token, "", "refresh")
					req.Header.Set("Origin", origin)
					// Host и forwarded-заголовки не должны превращать чужой Origin в доверенный.
					req.Host = "evil.example"
					req.Header.Set("X-Forwarded-Host", "evil.example")
					req.Header.Set("X-Forwarded-Proto", "https")
					rec := serveCSRF(handler, req)
					if origin == appOrigin {
						if rec.Code >= 400 {
							t.Fatalf("app origin blocked without CORS: %d %s", rec.Code, rec.Body)
						}
					} else {
						assertCSRFError(t, rec)
					}
					for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Expose-Headers"} {
						if rec.Header().Get(header) != "" {
							t.Fatalf("disabled CORS emitted %s", header)
						}
					}
				})
			}
		}
	}
}

func TestCSRFAppOriginBootstrapWithoutCORS(t *testing.T) {
	handler, csrf, users, _, expired := csrfRouterOrigins(t, nil, middleware.DefaultAppOrigin)
	req := csrfRequest("POST", baseURL+"/auth/refresh", csrf.Anonymous(), expired, "refresh")
	req.Header.Set("Origin", middleware.DefaultAppOrigin)
	rec := serveCSRF(handler, req)
	assertCSRFError(t, rec)
	cookie := responseCSRF(t, rec)
	if cookie == nil || !csrf.Valid(cookie.Value, 1) || users.mutations != 0 {
		t.Fatal("app origin could not safely restore signed CSRF without CORS")
	}
	req = csrfRequest("POST", baseURL+"/auth/refresh", cookie.Value, expired, "refresh")
	req.Header.Set("Origin", middleware.DefaultAppOrigin)
	rec = serveCSRF(handler, req)
	if rec.Code != 204 {
		t.Fatalf("restored refresh blocked without CORS: %d %s", rec.Code, rec.Body)
	}
}

func TestCSRFAdditionalCORSOrigin(t *testing.T) {
	const frontend = "http://localhost:5173"
	handler, csrf, _, _, _ := csrfRouterOrigins(t, []string{frontend}, middleware.DefaultAppOrigin)
	for _, origin := range []string{frontend, middleware.DefaultAppOrigin} {
		req := csrfRequest("POST", baseURL+"/auth/login", csrf.Anonymous(), "", "")
		req.Header.Set("Origin", origin)
		rec := serveCSRF(handler, req)
		if rec.Code != 200 {
			t.Fatalf("trusted origin blocked: %q %d %s", origin, rec.Code, rec.Body)
		}
		wantCORS := ""
		if origin == frontend {
			wantCORS = frontend
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != wantCORS {
			t.Fatal("CSRF trust changed CORS policy")
		}
	}
}
