// Package api содержит сгенерированные типы и HTTP-интерфейсы контракта spec/openapi.json.
package api

// Стабильные имена кодов для прикладного кода. Генератор добавляет префикс типа,
// когда одинаковые значения встречаются и в Error, и в NotebookEditingError.
const (
	S2sForbidden       = ErrorCodeS2sForbidden
	Internal           = ErrorCodeInternal
	InvalidCredentials = ErrorCodeInvalidCredentials
	// LoginTaken обозначает занятый логин в ответе API.
	LoginTaken = ErrorCodeLoginTaken
	// NotImplemented обозначает операцию, которая пока не реализована сервером.
	NotImplemented  = ErrorCodeNotImplemented
	ValidationError = ErrorCodeValidationError
)
