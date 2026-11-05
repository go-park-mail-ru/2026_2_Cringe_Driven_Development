package migration

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationCommandWithS3AndPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "migration_" + uuid.NewString()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); admin.Close() })
	databaseURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := databaseURL.Query()
	query.Set("search_path", schema)
	databaseURL.RawQuery = query.Encode()
	pool, err := pgxpool.New(ctx, databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(1,'owner','hash')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = pool.QueryRow(ctx, `INSERT INTO notebooks(owner_id,name) VALUES(1,'legacy') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notebook_legacy_sources(notebook_id,file_key) VALUES($1,'original.ipynb')`, id); err != nil {
		t.Fatal(err)
	}
	document := `{"nbformat":4,"nbformat_minor":5,"metadata":{"custom":true},"cells":[{"id":"stable","cell_type":"code","source":["  я","😀"],"outputs":[{"text":"saved"}],"execution_count":4,"attachments":{"keep":{}}}]}`
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/legacy/original.ipynb" {
			t.Errorf("unexpected S3 operation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/x-ipynb+json")
		_, _ = fmt.Fprint(w, document)
	}))
	defer source.Close()
	for key, value := range map[string]string{"AWS_REGION": "us-east-1", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_ENDPOINT_URL_S3": source.URL} {
		t.Setenv(key, value)
	}
	opts := Options{DatabaseURL: databaseURL.String(), Bucket: "legacy"}
	var out bytes.Buffer
	if err = RunCommand(ctx, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "migrated=1") || requests.Load() != 1 {
		t.Fatalf("%s requests=%d", out.String(), requests.Load())
	}
	repo := postgres.NewNotebookRepository(pool)
	n, err := repo.Get(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n.Cells[0].ID != "stable" || n.Cells[0].Source != "  я😀" || *n.Cells[0].ExecutionCount != 4 || n.PreviewCode != "  я😀" {
		t.Fatalf("migration: %+v", n)
	}
	revision := n.Revision
	out.Reset()
	if err = RunCommand(ctx, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipped=1") || requests.Load() != 1 {
		t.Fatalf("retry: %s requests=%d", out.String(), requests.Load())
	}
	opts.VerifyOnly = true
	out.Reset()
	if err = RunCommand(ctx, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verified=1") || requests.Load() != 2 {
		t.Fatalf("verify: %s requests=%d", out.String(), requests.Load())
	}
	n, err = repo.Get(ctx, id, 1)
	if err != nil || n.Revision != revision {
		t.Fatal("verification changed revision")
	}
}
