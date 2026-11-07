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
	ID             string
	Kind           CellKind
	Source         string
	SourceVersion  string
	Metadata       json.RawMessage
	Extra          map[string]json.RawMessage
	Outputs        json.RawMessage
	ExecutionCount *int64
	Attachments    json.RawMessage
	// Presence preserves optional fields during ipynb export.
	OutputTruncated    bool
	HasRuntimeMetadata bool
	HasExecutionCount  bool
}

// Notebook is a consistent snapshot of metadata, document fields and ordered cells.
type Notebook struct {
	ID          int64
	OwnerID     int64
	Name        string
	Revision    string
	PreviewCode string
	SizeBytes   int64
	Document    Document
	CellsCount  int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Cells       []Cell
}

// NotebookSummary нужен списку: только база, без файла.
type NotebookSummary struct {
	PreviewCode      string
	HasActiveSession bool
	ID               int64
	Name             string
	CellsCount       int
	UpdatedAt        time.Time
}

// Content — согласованный снимок содержимого и непрозрачного токена редакции.
type Content struct {
	Hash  string
	Cells []Cell
}

// CellChange возвращает изменённый блок и хеш новой редакции блокнота.
type CellChange struct {
	Hash string
	Cell Cell
}

// Document contains the top-level ipynb fields, without cells.
type Document struct {
	NBFormat      int
	NBFormatMinor int
	Metadata      json.RawMessage
	Extra         map[string]json.RawMessage
}
