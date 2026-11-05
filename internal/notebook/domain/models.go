// Package models содержит типы, которые передаются между слоями блокнотов.
package domain

import (
	"encoding/json"
	"time"
)

// CellKind совпадает с cell_type в файле .ipynb.
type CellKind string

const (
	CellKindCode     CellKind = "code"
	CellKindMarkdown CellKind = "markdown"
)

// Cell хранит блок; ID уникален внутри блокнота.
type Cell struct {
	// ID Идентификатор записи.
	ID string
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind CellKind
	// Source Исходный текст ячейки.
	Source string
	// Result Результат команды в исходном JSON-представлении.
	Result *CellResult
}

// Notebook собирается из строки в базе и блоков из файла.
type Notebook struct {
	ID         int64
	OwnerID    int64
	Name       string
	FileKey    string
	CellsCount int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Cells      []Cell
}

// NotebookSummary нужен списку: только база, без файла.
type NotebookSummary struct {
	ID         int64
	Name       string
	CellsCount int
	UpdatedAt  time.Time
}

// Content — согласованный снимок содержимого и хеша точных байтов ipynb.
type Content struct {
	Hash  string
	Cells []Cell
}

// CellChange возвращает изменённый блок и хеш новой редакции блокнота.
type CellChange struct {
	Hash string
	Cell Cell
}

// CellResult содержит сохранённые выводы, счётчик и признак усечения, независимые от версии исходников.
type CellResult struct {
	// Outputs Сохранённые сообщения вывода в формате ipynb.
	Outputs []json.RawMessage
	// ExecutionCount Счётчик исполнения kernel; nil означает отсутствие известного номера.
	ExecutionCount *int64
	// Truncated Признак усечения данных из-за ограничения размера.
	Truncated bool
}
