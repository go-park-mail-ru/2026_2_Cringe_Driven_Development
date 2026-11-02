// Package httptransport собирает HTTP-конвейер приложения.
package httptransport

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/security"
	"github.com/gorilla/mux"
)

const BaseURL = "/api/v1"

func NewRouter(srv Server, tokens security.TokenParser, sessions security.AccessSessions, bffKey string) (http.Handler, error) {
	if strings.TrimSpace(bffKey) == "" {
		return nil, errors.New("BFF_API_KEY is not set")
	}
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}

	r := mux.NewRouter()
	r.Use(middleware.RecordRoute)
	r.NotFoundHandler = http.HandlerFunc(httperr.NotFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(httperr.MethodNotAllowed)

	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Явно показываем линтеру и другим разработчикам, что игнорируем ошибку
		_, _ = w.Write([]byte(`{"status": "alive"}`))
	}).Methods(http.MethodGet)

	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:    BaseURL,
		BaseRouter: r,
		// S2S и Bearer проверяются до разбора параметров.
		Middlewares: []api.MiddlewareFunc{
			middleware.Validator(spec, BaseURL),
		},
		ErrorHandlerFunc: httperr.RequestError,
	})

	handler := security.S2S(bffKey, BaseURL)(security.AuthenticateBearer(tokens, sessions)(middleware.BodyLimit(r, notebookusecase.MaxSourceBytes, BaseURL+"/notebooks")))
	return middleware.RequestID(middleware.Logging(middleware.Recover(handler))), nil
}
