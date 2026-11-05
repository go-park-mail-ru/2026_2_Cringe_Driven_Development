package domain

import (
	"math"
	"strings"
	"testing"
)

// TestCommandLimits проверяет граничные размеры и допустимые значения таймаута команды.
func TestCommandLimits(t *testing.T) {
	for _, tc := range []struct {
		timeout float64
		valid   bool
	}{{1, true}, {300, true}, {86400, true}, {0, false}, {86401, false}, {math.NaN(), false}, {math.Inf(1), false}} {
		r := ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: strings.Repeat("a", 64), TimeoutSeconds: tc.timeout}
		if got := r.Validate() == nil; got != tc.valid {
			t.Errorf("timeout %v: valid=%v", tc.timeout, got)
		}
	}
	if _, err := CommandPayload(Command{ExecutionID: "run", Code: strings.Repeat("x", 1048577), TimeoutSeconds: 300}); err == nil {
		t.Fatal("source beyond agent character limit admitted")
	}
}
