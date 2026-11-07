package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

func TestStartupErrorLogsSafeCodeAndPreservesCause(t *testing.T) {
	for _, stage := range []string{"database_connect", "database_migrate", "s3_client", "redis_connect", "runtime_init", "http_listen", "notebook_migration", "totp_init"} {
		source := errors.New("SECRET-credential")
		err := startupError(stage, source)
		if !errors.Is(err, source) || observability.ErrorCode(err) != stage {
			t.Fatalf("stage %s: code=%s", stage, observability.ErrorCode(err))
		}
		var out bytes.Buffer
		slog.New(observability.NewHandler(slog.NewJSONHandler(&out, nil))).Error("application stopped", "error_code", observability.ErrorCode(err))
		if strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "request_id") {
			t.Fatalf("unsafe startup log: %s", out.String())
		}
		var entry map[string]any
		if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["error_code"] != stage {
			t.Fatalf("stage %s: unexpected logged code %v", stage, entry["error_code"])
		}
	}
	if got := observability.ErrorCode(startupError("unknown", errors.New("SECRET"))); got != "internal_error" {
		t.Fatalf("unknown stage code=%s", got)
	}
}
