package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreviewPreservesTextAndBounds(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{" \t\n", ""}, {"  print('я😀')\n2\n3\n4\n5\n6\n7", "  print('я😀')\n2\n3\n4\n5\n6"},
		{strings.Repeat("😀", 1100), strings.Repeat("😀", 1024)},
	} {
		got := Preview(tc.source)
		if got != tc.want || !utf8.ValidString(got) {
			t.Fatalf("preview %q", got)
		}
	}
}
