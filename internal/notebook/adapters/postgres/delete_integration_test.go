package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func deletionNotebook(t *testing.T, repo *NotebookRepository) domain.Notebook {
	t.Helper()
	n, err := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}).Create(context.Background(), 1, "delete me")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func deletionHistory(t *testing.T, pool *pgxpool.Pool, id int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	sid := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO runtime_sessions(id,owner_id,notebook_id,request_id,language,state,data) VALUES($1,1,$2,$3,'python','STOPPED','{"public":{"cleanup_pending":false},"CreateUnknown":false}')`, sid, id, sid.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime_executions(session_id,id,notebook_id,cell_id,attempt,data) VALUES($1,'execution',$2,'cell',1,'{"Public":{"state":"SUCCEEDED","projection":"applied"},"Dispatch":"DONE"}')`, sid, id); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO runtime_events(session_id,public_seq,kind,body) VALUES($1,1,'execution','{}')`,
		`INSERT INTO runtime_batches(session_id,request_id,request,result) VALUES($1,'batch','{}','{}')`,
	} {
		if _, err := pool.Exec(ctx, query, sid); err != nil {
			t.Fatal(err)
		}
	}
	return sid
}

func deletionCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	result := map[string]int{}
	for _, table := range []string{"notebooks", "notebook_cells", "notebook_cell_data", "runtime_sessions", "runtime_executions", "runtime_events", "runtime_batches", "runtime_projections"} {
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		result[table] = n
	}
	return result
}

func TestDeleteNotebookRemovesContentAndCompletedHistory(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	n := deletionNotebook(t, repo)
	deletionHistory(t, pool, n.ID)
	before, err := repo.ExecutionSource(ctx, n.ID, 1, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: n.Cells[0].ID, SourceVersion: before.SourceVersion, Outputs: []byte(`[{"output_type":"stream","name":"stdout","text":"done"}]`)}); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteNotebook(ctx, n.ID, 1); err != nil {
		t.Fatal(err)
	}
	for table, count := range deletionCounts(t, pool) {
		if count != 0 {
			t.Errorf("%s retained %d rows", table, count)
		}
	}
	if _, err = repo.Get(ctx, n.ID, 1); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
	if err = repo.DeleteNotebook(ctx, n.ID, 1); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
	if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: n.Cells[0].ID, SourceVersion: before.SourceVersion, Outputs: []byte(`[]`)}); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatalf("late result: %v", err)
	}
	list, err := repo.List(ctx, 1)
	if err != nil || len(list) != 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestDeleteNotebookRejectsBusyWithoutChanges(t *testing.T) {
	for name, query := range map[string]string{
		"active idle":        `UPDATE runtime_sessions SET state='READY' WHERE id=$1`,
		"active executing":   `UPDATE runtime_executions SET data='{"Public":{"state":"RUNNING"},"Dispatch":"SENT"}' WHERE session_id=$1`,
		"cleanup pending":    `UPDATE runtime_sessions SET data=jsonb_set(data,'{public,cleanup_pending}','true') WHERE id=$1`,
		"unknown create":     `UPDATE runtime_sessions SET data=jsonb_set(data,'{CreateUnknown}','true') WHERE id=$1`,
		"leased worker":      `UPDATE runtime_sessions SET lease_until=now()+interval '1 minute' WHERE id=$1`,
		"pending projection": `INSERT INTO runtime_projections(session_id,execution_id,version) VALUES($1,'execution',1)`,
	} {
		t.Run(name, func(t *testing.T) {
			repo, pool := testRepository(t)
			ctx := context.Background()
			n := deletionNotebook(t, repo)
			sid := deletionHistory(t, pool, n.ID)
			if _, err := pool.Exec(ctx, query, sid); err != nil {
				t.Fatal(err)
			}
			before := deletionCounts(t, pool)
			original, err := repo.Get(ctx, n.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.DeleteNotebook(ctx, n.ID, 1); !errors.Is(err, usecase.ErrNotebookInUse) {
				t.Fatalf("busy notebook: %v", err)
			}
			after, err := repo.Get(ctx, n.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, after) || !reflect.DeepEqual(before, deletionCounts(t, pool)) {
				t.Fatal("rejection changed data")
			}
			if err = repo.DeleteNotebook(ctx, n.ID, 2); !errors.Is(err, usecase.ErrNotebookNotFound) {
				t.Fatalf("foreign busy notebook: %v", err)
			}
		})
	}
}

func TestDeleteNotebookOtherNotebookAndRetryAfterCleanup(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	n := deletionNotebook(t, repo)
	other := deletionNotebook(t, repo)
	sid := deletionHistory(t, pool, n.ID)
	otherSID := deletionHistory(t, pool, other.ID)
	if _, err := pool.Exec(ctx, `UPDATE runtime_sessions SET state='READY' WHERE id=$1`, otherSID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime_sessions SET data=jsonb_set(data,'{public,cleanup_pending}','true') WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteNotebook(ctx, n.ID, 1); !errors.Is(err, usecase.ErrNotebookInUse) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime_sessions SET data=jsonb_set(data,'{public,cleanup_pending}','false') WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, n.ID, 1); err != nil {
		t.Fatal("cleanup automatically removed notebook", err)
	}
	if err := repo.DeleteNotebook(ctx, n.ID, 2); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
	if err := repo.DeleteNotebook(ctx, n.ID+100, 1); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
	if err := repo.DeleteNotebook(ctx, n.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, other.ID, 1); err != nil {
		t.Fatal("deleted unrelated notebook", err)
	}
}

func TestDeleteNotebookRollsBackHistoryOnLateFailure(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	n := deletionNotebook(t, repo)
	deletionHistory(t, pool, n.ID)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_notebook_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected delete failure'; END $$; CREATE TRIGGER reject_notebook_delete BEFORE DELETE ON notebooks FOR EACH ROW EXECUTE FUNCTION reject_notebook_delete()`); err != nil {
		t.Fatal(err)
	}
	before := deletionCounts(t, pool)
	if err := repo.DeleteNotebook(ctx, n.ID, 1); err == nil {
		t.Fatal("storage failure ignored")
	}
	if !reflect.DeepEqual(before, deletionCounts(t, pool)) {
		t.Fatal("partial deletion after rollback")
	}
	if _, err := repo.Get(ctx, n.ID, 1); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNotebookAndEditingSerialize(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	for range 8 {
		n := deletionNotebook(t, repo)
		start := make(chan struct{})
		changed := make(chan error, 1)
		go func() {
			<-start
			_, err := repo.Apply(ctx, n.ID, 1, n.Revision, usecase.Operation{Kind: usecase.Edit, CellID: n.Cells[0].ID, Source: "edited"})
			changed <- err
		}()
		close(start)
		if err := repo.DeleteNotebook(ctx, n.ID, 1); err != nil {
			t.Fatal(err)
		}
		if err := <-changed; err != nil && !errors.Is(err, usecase.ErrNotebookNotFound) {
			t.Fatalf("edit/delete: %v", err)
		}
		if _, err := repo.Get(ctx, n.ID, 1); !errors.Is(err, usecase.ErrNotebookNotFound) {
			t.Fatal("editing recreated notebook", err)
		}
	}
}
