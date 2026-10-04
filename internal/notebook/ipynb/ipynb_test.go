package ipynb

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    []models.Cell
		wantErr bool
		// errIs проверяется, только если задан.
		errIs error
	}{
		{
			name: "source строкой, оба вида блоков",
			data: `{"cells": [
				{"id": "a", "cell_type": "code", "source": "x = 1", "outputs": [], "execution_count": null},
				{"id": "b", "cell_type": "markdown", "source": "# Заголовок"}
			]}`,
			want: []models.Cell{
				{ID: "a", Kind: models.CellKindCode, Source: "x = 1"},
				{ID: "b", Kind: models.CellKindMarkdown, Source: "# Заголовок"},
			},
		},
		{
			name: "source массивом строк",
			data: `{"cells": [{"id": "a", "cell_type": "code", "source": ["x = 1\n", "y = 2"]}]}`,
			want: []models.Cell{{ID: "a", Kind: models.CellKindCode, Source: "x = 1\ny = 2"}},
		},
		{
			name: "пустой cells",
			data: `{"cells": []}`,
			want: []models.Cell{},
		},
		{
			name:    "битый JSON",
			data:    `{"cells": [`,
			wantErr: true,
		},
		{
			name:    "source не строка и не массив",
			data:    `{"cells": [{"id": "a", "cell_type": "code", "source": 42}]}`,
			wantErr: true,
		},
		{
			name:    "незнакомый cell_type",
			data:    `{"cells": [{"id": "a", "cell_type": "raw", "source": ""}]}`,
			wantErr: true,
			errIs:   ErrUnknownCellType,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse() = %v, want error", got)
				}
				if tt.errIs != nil && !errors.Is(err, tt.errIs) {
					t.Fatalf("Parse() error = %v, want %v", err, tt.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Parse() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	cells := []models.Cell{
		{ID: "a", Kind: models.CellKindCode, Source: "print(1)\n"},
		{ID: "b", Kind: models.CellKindMarkdown, Source: "# Заголовок"},
	}
	data, err := Build(cells)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() собранного файла: %v", err)
	}
	if diff := cmp.Diff(cells, got); diff != "" {
		t.Errorf("блоки после сборки и разбора (-want +got):\n%s", diff)
	}

	var raw struct {
		Cells         []map[string]json.RawMessage `json:"cells"`
		NBFormat      int                          `json:"nbformat"`
		NBFormatMinor int                          `json:"nbformat_minor"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.NBFormat != 4 || raw.NBFormatMinor != 5 {
		t.Errorf("nbformat = %d.%d, want 4.5", raw.NBFormat, raw.NBFormatMinor)
	}
	code, markdown := raw.Cells[0], raw.Cells[1]
	if string(code["outputs"]) != "[]" || string(code["execution_count"]) != "null" {
		t.Errorf("у блока кода outputs = %s, execution_count = %s; want [] и null",
			code["outputs"], code["execution_count"])
	}
	for _, key := range []string{"outputs", "execution_count"} {
		if _, ok := markdown[key]; ok {
			t.Errorf("у блока markdown есть %q", key)
		}
	}
}

func TestBuildUnknownKind(t *testing.T) {
	_, err := Build([]models.Cell{{ID: "a", Kind: "python"}})
	if !errors.Is(err, ErrUnknownCellType) {
		t.Errorf("Build() error = %v, want ErrUnknownCellType", err)
	}
}
