package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

var errNoUser = errors.New("authentication required")

// Validator checks requests against the contract: bodies, parameters and
// the security of each operation. prefix is the part of the path that is
// not in the contract, such as /api/v1.
func Validator(spec *openapi3.T, prefix string) func(http.Handler) http.Handler {
	// Keep validation messages short: without this they include the whole schema.
	openapi3.SchemaErrorDetailsDisabled = true

	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Prefix:               prefix,
		DoNotValidateServers: true,
		Options: openapi3filter.Options{
			AuthenticationFunc: requireUser,
		},
		ErrorHandlerWithOpts: validationError,
	})
}

// requireUser is called for operations with security in the contract.
// The user was already put into the context by Authenticate.
func requireUser(_ context.Context, input *openapi3filter.AuthenticationInput) error {
	if _, ok := auth.UserIDFromContext(input.RequestValidationInput.Request.Context()); !ok {
		return errNoUser
	}
	return nil
}

func validationError(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
	var securityErr *openapi3filter.SecurityRequirementsError
	switch {
	case errors.As(err, &securityErr):
		httperr.Write(w, http.StatusUnauthorized, api.Unauthorized, "authentication required")
	case opts.StatusCode == http.StatusNotFound:
		httperr.Write(w, http.StatusNotFound, api.NotFound, "no such endpoint")
	default:
		httperr.Write(w, http.StatusBadRequest, api.ValidationError, err.Error())
	}
}
