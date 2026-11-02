// Package api содержит сгенерированные типы и HTTP-интерфейсы контракта Apidog.
package api

// Стабильные имена кодов для прикладного кода. Генератор добавляет префикс типа,
// когда одинаковые значения встречаются и в Error, и в NotebookEditingError.
const (
	S2sForbidden       = ErrorCodeS2sForbidden
	Internal           = ErrorCodeInternal
	InvalidCredentials = ErrorCodeInvalidCredentials
	LoginTaken         = ErrorCodeLoginTaken
	NotFound           = ErrorCodeNotFound
	NotImplemented     = ErrorCodeNotImplemented
	Unauthorized       = ErrorCodeUnauthorized
	ValidationError    = ErrorCodeValidationError
)
