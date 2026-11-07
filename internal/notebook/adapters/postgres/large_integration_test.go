package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

// Opt in: this allocates full-size documents and performs two real minute-long
// PostgreSQL runs. It is deliberately separate from the ordinary unit suite.
func TestLargeNotebookAccountingAndAutosave(t *testing.T) {
	if os.Getenv("NOTEBOOK_LARGE_TEST") != "1" {
		t.Skip("set NOTEBOOK_LARGE_TEST=1 for 128 MiB acceptance/performance test")
	}
	repo, pool := testRepository(t)
	ctx := context.Background()
	// Incompressible base64, split between outputs and attachments. This exercises
	// TOAST storage rather than a highly compressible repeated-string fixture.
	random := make([]byte, 96*1024*1024)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	payload := base64.RawStdEncoding.EncodeToString(random)
	random = nil
	doc := domain.Document{NBFormat: 4, NBFormatMinor: 5, Metadata: json.RawMessage(`{"unicode":"я😀","quote":"\"\\"}`)}
	cells := []domain.Cell{
		{ID: "edit", Kind: domain.CellKindCode, Source: "x=0", Metadata: json.RawMessage(`{}`), Outputs: json.RawMessage(`[]`), HasExecutionCount: true},
		{ID: "heavy", Kind: domain.CellKindCode, Source: "heavy", Metadata: json.RawMessage(`{}`), Outputs: json.RawMessage(`[{"output_type":"display_data","data":{"text/plain":"` + payload[:16*1024*1024] + `"}}]`), HasExecutionCount: true},
		{ID: "attached", Kind: domain.CellKindMarkdown, Source: "attachment", Metadata: json.RawMessage(`{}`), Attachments: json.RawMessage(`{"blob":{"application/octet-stream":"` + payload[16*1024*1024:] + `"}}`)},
	}
	// Trim only payload bytes to make the canonical export exactly the limit.
	size := ipynb.DocumentSize(ipynb.HeaderSize(doc), []int64{ipynb.CellSize(cells[0]), ipynb.CellSize(cells[1]), ipynb.CellSize(cells[2])})
	excess := size - usecase.MaxNotebookBytes
	if excess <= 0 {
		t.Fatal("fixture must exceed the bound before trimming")
	}
	attachment := string(cells[2].Attachments)
	cut := len(attachment) - 3 - int(excess)
	cells[2].Attachments = json.RawMessage(attachment[:cut] + attachment[cut+int(excess):])
	payload = ""
	attachment = ""
	n, err := repo.Create(ctx, domain.Notebook{OwnerID: 1, Name: "large", Document: doc, Cells: cells})
	if err != nil {
		t.Fatal(err)
	}
	if n.SizeBytes != usecase.MaxNotebookBytes {
		t.Fatalf("boundary fixture: %d", n.SizeBytes)
	}
	export, err := ipynb.EncodeDocument(n.Document, n.Cells)
	if err != nil || len(export) != usecase.MaxNotebookBytes {
		t.Fatalf("exact boundary export: %d %v", len(export), err)
	}
	export = nil
	cells = nil
	n.Cells = nil
	// A one-byte expansion must fail without changing data or revision.
	if _, err = repo.Apply(ctx, n.ID, 1, n.Revision, usecase.Operation{Kind: usecase.Edit, CellID: "edit", Source: "x=00"}); !errors.Is(err, usecase.ErrTooLarge) {
		t.Fatalf("source overflow: %v", err)
	}
	var revision string
	var bytes int64
	if err = pool.QueryRow(ctx, `SELECT revision,size_bytes FROM notebooks WHERE id=$1`, n.ID).Scan(&revision, &bytes); err != nil || revision != n.Revision || bytes != n.SizeBytes {
		t.Fatal("overflow changed notebook")
	}
	src, err := repo.ExecutionSource(ctx, n.ID, 1, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: "edit", SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"too much"}]`)}); !errors.Is(err, usecase.ErrTooLarge) {
		t.Fatalf("result overflow: %v", err)
	}

	// Fixed-width changes keep the full document exactly at the boundary while
	// preserving both the heavy output row and the attachment row.
	var heavyRowsBefore, heavyRowsAfter string
	const physicalRows = `SELECT string_agg(cell_id||':'||xmin::text||':'||ctid::text,',' ORDER BY cell_id) FROM notebook_cell_data WHERE notebook_id=$1 AND cell_id IN ('heavy','attached')`
	if err = pool.QueryRow(ctx, physicalRows, n.ID).Scan(&heavyRowsBefore); err != nil {
		t.Fatal(err)
	}
	small, err := repo.Create(ctx, domain.Notebook{OwnerID: 1, Name: "small", Document: doc, Cells: []domain.Cell{
		{ID: "edit", Kind: domain.CellKindCode, Source: "x=0", Metadata: json.RawMessage(`{}`), Outputs: json.RawMessage(`[]`), HasExecutionCount: true},
		{ID: "heavy", Kind: domain.CellKindCode, Source: "heavy", Outputs: json.RawMessage(`[]`), HasExecutionCount: true},
		{ID: "attached", Kind: domain.CellKindMarkdown, Source: "attachment"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var version string
	var sharedBuffers string
	_ = pool.QueryRow(ctx, `SELECT version(),current_setting('shared_buffers')`).Scan(&version, &sharedBuffers)
	t.Logf("stand: %s shared_buffers=%s CPUs=%d", version, sharedBuffers, runtime.NumCPU())
	debug.FreeOSMemory()
	for _, item := range []struct {
		name string
		n    domain.Notebook
	}{{"large", n}, {"small", small}} {
		debug.FreeOSMemory()
		durations := make([]time.Duration, 0, 60)
		rev := item.n.Revision
		var walStart, walEnd string
		if err = pool.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text`).Scan(&walStart); err != nil {
			t.Fatal(err)
		}
		rssBefore, maxRSS := processRSS(), processRSS()
		start := time.Now()
		tracer, _ := pool.Config().ConnConfig.Tracer.(*sqlTrace)
		if tracer != nil {
			tracer.enable(true)
		}
		for i := 0; i < 60; i++ {
			time.Sleep(time.Until(start.Add(time.Duration(i) * time.Second)))
			began := time.Now()
			changed, e := repo.Apply(ctx, item.n.ID, 1, rev, usecase.Operation{Kind: usecase.Edit, CellID: "edit", Source: fmt.Sprintf("%03d", i)})
			durations = append(durations, time.Since(began))
			if e != nil {
				t.Fatal(e)
			}
			rev = changed.Hash
			if rss := processRSS(); rss > maxRSS {
				maxRSS = rss
			}
		}
		if tracer != nil {
			tracer.enable(false)
		}
		time.Sleep(time.Until(start.Add(time.Minute)))
		if err = pool.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text`).Scan(&walEnd); err != nil {
			t.Fatal(err)
		}
		var walBytes int64
		if err = pool.QueryRow(ctx, `SELECT pg_wal_lsn_diff($1::pg_lsn,$2::pg_lsn)::bigint`, walEnd, walStart).Scan(&walBytes); err != nil {
			t.Fatal(err)
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		t.Logf("autosave %s: bytes=%d samples=60 p50=%s p95=%s rss_before=%d rss_peak=%d WAL=%d", item.name, item.n.SizeBytes, durations[29], durations[56], rssBefore, maxRSS, walBytes)
	}
	if err = pool.QueryRow(ctx, physicalRows, n.ID).Scan(&heavyRowsAfter); err != nil || heavyRowsBefore != heavyRowsAfter {
		t.Fatalf("unchanged heavy rows were rewritten: %s -> %s %v", heavyRowsBefore, heavyRowsAfter, err)
	}
	t.Logf("unchanged heavy data xmin/ctid: %s", heavyRowsAfter)
	var physicalBytes int64
	if err = pool.QueryRow(ctx, `SELECT sum(COALESCE(pg_column_size(outputs),0)+COALESCE(pg_column_size(attachments),0)) FROM notebook_cell_data WHERE notebook_id=$1`, n.ID).Scan(&physicalBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("physical JSONB outputs/attachments bytes=%d", physicalBytes)

}

// Kept here so RSS is measured from the process rather than inferred from heap.
func processRSS() int64 {
	b, _ := os.ReadFile("/proc/self/statm")
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}
