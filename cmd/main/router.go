package main

import (
	"fmt"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery"
	"github.com/gorilla/mux"
)

const baseURL = "/api/v1"

// Оба типа называются Handler, а встроить в структуру два поля с одним именем
// нельзя. Псевдонимы дают встроенным полям разные имена.
type (
	userHandler     = userdelivery.Handler
	notebookHandler = notebookdelivery.Handler
)

// server реализует api.StrictServerInterface: каждый встроенный хендлер
// приносит методы своей части контракта.
type server struct {
	*userHandler
	*notebookHandler
}

var _ api.StrictServerInterface = server{}

func newRouter(srv server, tokens middleware.TokenParser, corsOrigins []string) (http.Handler, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}

	r := mux.NewRouter()
	r.NotFoundHandler = http.HandlerFunc(httperr.NotFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(httperr.MethodNotAllowed)

	// Первый в списке внешний: запрос сначала попадает в него.
	r.Use(middleware.RequestID, middleware.Logging, middleware.Recover)

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
		BaseURL:    baseURL,
		BaseRouter: r,
		// Только для ручек контракта. Здесь внешний последний в списке,
		// поэтому Authenticate выполняется раньше Validator.
		Middlewares: []api.MiddlewareFunc{
			middleware.Validator(spec, baseURL),
			middleware.Authenticate(tokens),
		},
		ErrorHandlerFunc: httperr.RequestError,
	})

	return middleware.CORS(corsOrigins)(r), nil
}
