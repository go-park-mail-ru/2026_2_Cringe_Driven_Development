package usecase

import "io"

// FileRequest передаёт ограниченный запрос файловому порту без адреса и токена агента.
type FileRequest struct {
	// Method задаёт транспортный метод текущего адаптера.
	Method string
	// Path содержит внутренний маршрут без адреса и credentials.
	Path string
	// Query хранит проверенные параметры части или страницы событий.
	Query string
	// Headers содержит ограниченные metadata без пользовательских токенов.
	Headers map[string]string
	// Body читается потоково, владение исходным потоком сохраняется у delivery.
	Body io.Reader
	// Length задаёт длину Body в байтах, -1 означает неизвестную длину.
	Length int64
}

// FileResponse владеет потоковым телом. Вызывающий обязательно закрывает Body.
type FileResponse struct {
	Status  int
	Headers map[string]string
	Body    io.ReadCloser
}
