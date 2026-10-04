// Package models содержит типы, которые передаются между слоями блокнотов.
package models

import "time"

// CellKind совпадает с cell_type в файле .ipynb.
type CellKind string

const (
	CellKindCode     CellKind = "code"
	CellKindMarkdown CellKind = "markdown"
)

// Cell хранит блок; ID уникален внутри блокнота.
type Cell struct {
	ID     string
	Kind   CellKind
	Source string
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
