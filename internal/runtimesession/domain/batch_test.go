package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestBatchValidationRejectsDuplicatesAndInvalidSnapshots проверяет отклонение пустого пакета, повторов
// ячеек и неверных параметров снимка.
func TestBatchValidationRejectsDuplicatesAndInvalidSnapshots(t *testing.T) {
	good := BatchRequest{RequestID: "batch-1", Cells: []BatchCell{{CellID: "a", SourceHash: strings.Repeat("a", 64)}}, TimeoutSeconds: 300}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BatchRequest){
		func(r *BatchRequest) { r.Cells = nil },
		func(r *BatchRequest) { r.Cells = append(r.Cells, r.Cells[0]) },
		func(r *BatchRequest) { r.RequestID = "" },
		func(r *BatchRequest) { r.TimeoutSeconds = 0 },
		func(r *BatchRequest) { r.Cells = []BatchCell{{CellID: "a", SourceHash: "bad"}} },
	} {
		r := good
		r.Cells = append([]BatchCell(nil), good.Cells...)
		change(&r)
		if !errors.Is(r.Validate(), ErrInvalid) {
			t.Fatal("invalid batch accepted")
		}
	}
}
