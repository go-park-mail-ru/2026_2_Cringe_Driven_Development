package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

var errNoUser = errors.New("authentication required")
var errUnsupportedSecurity = errors.New("unsupported authentication scheme")

// Validator сверяет запрос с контрактом: тело, параметры и security ручки.
// prefix — часть пути, которой нет в контракте, например /api/v1.
func Validator(spec *openapi3.T, prefix string) func(http.Handler) http.Handler {
	// Без этого в текст ошибки попадает вся схема целиком.
	openapi3.SchemaErrorDetailsDisabled = true

	// kin-openapi буферизует тело и при валидации security. Аутентификация
	// явно проверяется ниже; валидация заголовков остаётся без повторного чтения.
	streamSpec := *spec
	streamSpec.Paths = openapi3.NewPaths()
	for path, item := range spec.Paths.Map() {
		streamSpec.Paths.Set(path, item)
	}
	if original := spec.Paths.Value("/users/me/avatar"); original != nil && original.Put != nil {
		item := *original
		operation := *original.Put
		security := openapi3.SecurityRequirements{}
		operation.Security = &security
		item.Put = &operation
		streamSpec.Paths.Set("/users/me/avatar", &item)
	}
	full, stream := validator(spec, prefix, false), validator(&streamSpec, prefix, true)
	return func(next http.Handler) http.Handler {
		fullHandler, streamHandler := full(next), stream(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && r.URL.Path == prefix+"/users/me/avatar" {
				if _, ok := principal.UserIDFromContext(r.Context()); !ok {
					httperr.Write(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "authentication required")
					return
				}
				streamHandler.ServeHTTP(w, r)
				return
			}
			fullHandler.ServeHTTP(w, r)
		})
	}
}

func validator(spec *openapi3.T, prefix string, excludeBody bool) func(http.Handler) http.Handler {
	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Prefix:               prefix,
		DoNotValidateServers: true,
		Options: openapi3filter.Options{
			AuthenticationFunc: requireUser,
			ExcludeRequestBody: excludeBody,
		},
		ErrorHandlerWithOpts: validationError,
	})
}

// requireUser вызывается для ручек, у которых в контракте есть security.
// Пользователя в контекст к этому моменту уже положил AuthenticateBearer.
func requireUser(_ context.Context, input *openapi3filter.AuthenticationInput) error {
	scheme := input.SecurityScheme
	if scheme == nil {
		return errUnsupportedSecurity
	}
	bearer := input.SecuritySchemeName == "bearerAuth" && scheme.Type == "http" && scheme.Scheme == "bearer"
	if !bearer {
		return errUnsupportedSecurity
	}

	if _, ok := principal.UserIDFromContext(input.RequestValidationInput.Request.Context()); !ok {
		return errNoUser
	}
	return nil
}

func validationError(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
	var securityErr *openapi3filter.SecurityRequirementsError
	switch {
	case errors.As(err, &securityErr):
		httperr.Write(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "authentication required")
	case opts.StatusCode == http.StatusNotFound:
		httperr.Write(w, http.StatusNotFound, api.ErrorCodeNotFound, "no such endpoint")
	default:
		httperr.Write(w, http.StatusBadRequest, api.ErrorCodeValidationError, err.Error())
	}
}
