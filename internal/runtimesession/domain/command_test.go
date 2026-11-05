package domain

import (
	"strings"
	"testing"
)

// TestCommandUsesUTF8WithoutUnnecessaryHTMLEscaping проверяет корректное кодирование UTF-8 без раздувания
// тела HTML-экранированием.
func TestCommandUsesUTF8WithoutUnnecessaryHTMLEscaping(t *testing.T) {
	payload, e := CommandPayload(Command{ExecutionID: "x", Code: "#" + strings.Repeat("<", 400000), TimeoutSeconds: 300})
	if e != nil || len(payload) > 500000 {
		t.Fatal(len(payload), e)
	}
	if _, e = CommandPayload(Command{ExecutionID: "x", Code: strings.Repeat("😀", 600000), TimeoutSeconds: 300}); e == nil {
		t.Fatal("wire limit ignored")
	}
}
