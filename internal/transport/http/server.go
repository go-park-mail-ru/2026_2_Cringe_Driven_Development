package httptransport

import (
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	runtimedelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
)

// Оба типа называются Handler, а встроить в структуру два поля с одним именем
// нельзя. Псевдонимы дают встроенным полям разные имена.
type (
	UserHandler     = userdelivery.Handler
	NotebookHandler = notebookdelivery.Handler
	// RuntimeHandler позволяет встроить обработчик runtime в общий сервер рядом с другими Handler.
	RuntimeHandler = runtimedelivery.Handler
)

// Server реализует api.StrictServerInterface: каждый встроенный хендлер
// приносит методы своей части контракта.
type Server struct {
	*UserHandler
	*NotebookHandler
	*RuntimeHandler
}
