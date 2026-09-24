package main

import (
	"fmt"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user"
	"github.com/gorilla/mux"
)

const baseURL = "/api/v1"

// Both handlers are named Handler, and a struct cannot embed two fields with
// the same name. Aliases give the embedded fields different names.
type (
	userHandler     = user.Handler
	notebookHandler = notebook.Handler
)

// server implements api.StrictServerInterface: each embedded handler
// brings the methods of its part of the contract.
type server struct {
	*userHandler
	*notebookHandler
}

var _ api.StrictServerInterface = server{}

func newRouter(srv server, tokens middleware.TokenParser) (http.Handler, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}

	r := mux.NewRouter()
	r.NotFoundHandler = http.HandlerFunc(httperr.NotFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(httperr.MethodNotAllowed)

	// The first one is the outermost: it sees the request first.
	r.Use(middleware.RequestID, middleware.Logging, middleware.Recover)

	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "alive"}`))
	}).Methods(http.MethodGet)

	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:    baseURL,
		BaseRouter: r,
		// Only for the operations of the contract. Here the last one is the
		// outermost, so Authenticate runs before Validator.
		Middlewares: []api.MiddlewareFunc{
			middleware.Validator(spec, baseURL),
			middleware.Authenticate(tokens),
		},
		ErrorHandlerFunc: httperr.RequestError,
	})

	return r, nil
}
