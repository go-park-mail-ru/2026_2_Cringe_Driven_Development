package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	nbpg "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	nbhttp "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	nbuc "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	runtime "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime"
	env "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/adapters/environment"
	storepg "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/adapters/postgres"
	delivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	UC "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	transport "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http"
	userpg "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/postgres"
	userredis "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/redis"
	security "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userhttp "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	useruc "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// TestRuntimeFullHTTPFlowWithFakeEnvironment проверяет полный HTTP-путь с настоящими хранилищами; окружение
// тестовое, а агент может быть настоящим при RUNTIME_TEST_REAL_AGENT=1.
func TestRuntimeFullHTTPFlowWithFakeEnvironment(t *testing.T) {
	dsn, redisAddr := os.Getenv("RUNTIME_TEST_DATABASE_URL"), os.Getenv("REDIS_TEST_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("set RUNTIME_TEST_DATABASE_URL, REDIS_TEST_ADDR for isolated integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "api13_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schema := pgx.Identifier{name}.Sanitize()
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rc := redis.NewClient(&redis.Options{Addr: redisAddr, MaxRetries: -1})
	defer func() { _ = rc.Close() }()
	prefix := name + ":"
	sessions := userredis.NewStore(rc, prefix, time.Second)
	defer func() {
		keys, _ := rc.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			_ = rc.Del(context.Background(), keys...).Err()
		}
	}()
	issuer := security.NewAccessToken([]byte("api13-integration-local-key"), time.Hour)
	users := useruc.NewUserUsecase(userpg.NewUserRepository(pool), sessions, issuer, security.PasswordHasher{}, security.RefreshTokens{}, time.Hour)
	store := storepg.New(pool)
	notebooks := nbuc.NewNotebookUsecase(nbpg.NewNotebookRepository(pool), store)
	module, err := runtime.Build(ctx, config.RuntimeConfig{Provider: "fake", SecretsKey: make([]byte, 32), MaxActive: 5, MaxConcurrentOps: 2, StartupTimeout: time.Minute, WorkDir: t.TempDir()}, pool)
	if err != nil {
		t.Fatal(err)
	}
	sessionAgent := module.Agent
	var renameAgent *renameSessionAgent
	if os.Getenv("RUNTIME_TEST_REAL_AGENT") == "1" {
		sessionAgent = newRealSessionAgent(t, func(ctx context.Context, id uuid.UUID) (string, error) {
			var language string
			queryErr := pool.QueryRow(ctx, "SELECT language FROM runtime_sessions WHERE id=$1", id).Scan(&language)
			return language, queryErr
		})
	} else {
		renameAgent = &renameSessionAgent{SessionAgent: sessionAgent, release: make(chan struct{})}
		sessionAgent = renameAgent
		t.Cleanup(renameAgent.finish)
	}
	service := UC.NewService(store, nil, sessionAgent)
	worker := UC.NewWorker(store, env.New(module.Environments), sessionAgent, nil)
	handler, err := transport.NewRouter(transport.Server{UserHandler: userhttp.NewHandler(users), NotebookHandler: nbhttp.NewHandler(notebooks), RuntimeHandler: delivery.NewHandler(service)}, issuer, sessions, "test-bff-key")
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = module.Worker.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	user, auth, err := users.Register(ctx, "runtime-user", "local-password")
	if err != nil {
		t.Fatal(err)
	}
	first, err := notebooks.Create(ctx, user.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := notebooks.Create(ctx, user.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	content, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	cell, err := notebooks.AddCell(ctx, first.ID, user.ID, content.Hash, "code", "print(42)", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, key, token string, hash ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, transport.BaseURL+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("X-BFF-Key", key)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if len(hash) > 0 {
			r.Header.Set("If-Match", `"`+hash[0]+`"`)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	call := func(method, path, body string, want int, hash ...string) *httptest.ResponseRecorder {
		t.Helper()
		w := request(method, path, body, "test-bff-key", auth.AccessToken, hash...)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	if w := request("POST", "/notebooks/1/runtime-sessions", "{", "wrong", "broken"); w.Code != 403 {
		t.Fatal("S2S did not precede body and bearer", w.Code)
	}
	if w := request("GET", "/runtime-sessions/active", "", "test-bff-key", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("GET", "/runtime-sessions/active", "", 200)
	if strings.TrimSpace(w.Body.String()) != `{"active_session":null}` {
		t.Fatal(w.Body.String())
	}
	startPath := fmt.Sprintf("/notebooks/%d/runtime-sessions", first.ID)
	startBody := `{"request_id":"start-first","language":"python"}`
	call("POST", startPath, `{"request_id":"start-r","language":"r"}`, 400)
	w = call("POST", startPath, startBody, 202)
	var session struct {
		ID uuid.UUID `json:"id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	w = call("POST", startPath, startBody, 202)
	if !strings.Contains(w.Body.String(), session.ID.String()) {
		t.Fatal("start repeated as new session")
	}
	w = call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"start-second","language":"python"}`, 409)
	if !strings.Contains(w.Body.String(), `"active_session"`) || !strings.Contains(w.Body.String(), session.ID.String()) {
		t.Fatal(w.Body.String())
	}
	pump := func() {
		t.Helper()
		if _, e := pool.Exec(ctx, "UPDATE runtime_sessions SET next_check=now() WHERE state NOT IN ('STOPPED','FAILED')"); e != nil {
			t.Fatal(e)
		}
		l, e := store.Claim(ctx, "integration")
		if e == nil {
			if e = worker.Step(ctx, l); e != nil {
				t.Fatal(e)
			}
		}
		if e = store.Project(ctx); e != nil && !errors.Is(e, D.ErrNotFound) {
			t.Fatal(e)
		}
	}
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			pump()
			if ready() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("runtime condition timed out")
	}
	wait(func() bool { v, e := store.Owned(ctx, user.ID, session.ID); return e == nil && v.State == D.Ready })
	assertBadge := func(want int64) {
		t.Helper()
		response := call("GET", "/notebooks", "", 200)
		var list []struct {
			ID     int64 `json:"id"`
			Active bool  `json:"has_active_session"`
		}
		if decodeErr := json.Unmarshal(response.Body.Bytes(), &list); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		for _, item := range list {
			if item.Active != (want != 0 && item.ID == want) {
				t.Fatalf("wrong runtime badge: %+v", item)
			}
		}
	}
	assertBadge(first.ID)
	base := "/runtime-sessions/" + session.ID.String()
	contentPath := fmt.Sprintf("/notebooks/%d/content", first.ID)
	beforeETag := call("GET", contentPath, "", 200).Header().Get("ETag")
	sum := sha256.Sum256([]byte("print(42)"))
	executeBody := fmt.Sprintf(`{"execution_id":"run","cell_id":%q,"source_hash":%q}`, cell.Cell.ID, hex.EncodeToString(sum[:]))
	call("POST", base+"/executions", executeBody, 202)
	if renameAgent != nil {
		wait(func() bool {
			x, e := store.Execution(ctx, user.ID, session.ID, "run")
			return e == nil && x.State == "RUNNING"
		})
	}
	beforeRename, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessionBeforeRename, err := store.Owned(ctx, user.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var controlsBeforeRename int64
	if renameAgent != nil {
		controlsBeforeRename = renameAgent.controls.Load()
	}
	const renamedName = "renamed during execution"
	call("PATCH", fmt.Sprintf("/notebooks/%d", first.ID), `{"name":"  renamed during execution  "}`, 200)
	afterRename, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil || !reflect.DeepEqual(beforeRename, afterRename) {
		t.Fatal("rename changed notebook content", err)
	}
	if call("GET", contentPath, "", 200).Header().Get("ETag") != beforeETag {
		t.Fatal("rename changed ETag")
	}
	sessionAfterRename, err := store.Owned(ctx, user.ID, session.ID)
	if err != nil || !reflect.DeepEqual(sessionBeforeRename, sessionAfterRename) {
		t.Fatal("rename changed runtime session", err)
	}
	if renameAgent != nil && renameAgent.controls.Load() != controlsBeforeRename {
		t.Fatal("rename sent a runtime control command")
	}
	assertRenamed := func() {
		t.Helper()
		var got struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if e := json.Unmarshal(call("GET", fmt.Sprintf("/notebooks/%d", first.ID), "", 200).Body.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if got.ID != first.ID || got.Name != renamedName {
			t.Fatalf("renamed notebook: %+v", got)
		}
		var list []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if e := json.Unmarshal(call("GET", "/notebooks", "", 200).Body.Bytes(), &list); e != nil {
			t.Fatal(e)
		}
		found := false
		for _, n := range list {
			if n.ID == first.ID {
				found = true
				if n.Name != renamedName {
					t.Fatal("list lost renamed name")
				}
			}
		}
		if !found {
			t.Fatal("renamed notebook missing from list")
		}
	}
	assertRenamed()
	assertBadge(first.ID)
	if renameAgent != nil {
		renameAgent.finish()
	}
	wait(func() bool {
		x, e := store.Execution(ctx, user.ID, session.ID, "run")
		return e == nil && x.State == "SUCCEEDED" && x.Projection == "applied"
	})
	assertBadge(first.ID)
	assertRenamed()
	w = call("GET", base+"/executions/run", "", 200)
	if !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", base+"/events?after=0", "", 200)
	if !strings.Contains(w.Body.String(), `"next_cursor"`) || !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal(w.Body.String())
	}

	saved, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Hash != cell.Hash {
		t.Fatal("output changed the notebook revision")
	}
	w = call("GET", contentPath, "", 200)
	if beforeETag == "" || w.Header().Get("ETag") != beforeETag {
		t.Fatal("output changed the content ETag")
	}
	if !strings.Contains(w.Body.String(), `"output_truncated":false`) || !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal("saved content result missing", w.Body.String())
	}
	// Каждое поле результата независимо остаётся вне hash/ETag редактора.
	resultRepo := nbpg.NewNotebookRepository(pool)
	executionSource, err := resultRepo.ExecutionSource(ctx, first.ID, user.ID, cell.Cell.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := int64(12)
	for _, result := range []nbuc.CellResult{
		{Outputs: json.RawMessage(`[{"text":"42\n43\n"}]`)},
		{Outputs: json.RawMessage(`[{"text":"42\n43\n"}]`), ExecutionCount: &count},
		{Outputs: json.RawMessage(`[{"text":"42\n43\n"}]`), ExecutionCount: &count, OutputTruncated: true},
	} {
		result.CellID, result.SourceVersion = cell.Cell.ID, executionSource.SourceVersion
		if _, err = resultRepo.SaveResult(ctx, first.ID, user.ID, result); err != nil {
			t.Fatal(err)
		}
		w = call("GET", contentPath, "", 200)
		var snapshot struct {
			Hash  string `json:"hash"`
			Cells []struct {
				ID              string          `json:"id"`
				Outputs         json.RawMessage `json:"outputs"`
				ExecutionCount  *int64          `json:"execution_count"`
				OutputTruncated bool            `json:"output_truncated"`
			} `json:"cells"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || snapshot.Hash != cell.Hash || w.Header().Get("ETag") != beforeETag {
			t.Fatal("result-only change invalidated content hash/ETag", err, w.Body.String())
		}
		found := false
		for _, c := range snapshot.Cells {
			if c.ID == cell.Cell.ID {
				found = true
				if string(c.Outputs) != string(result.Outputs) || (c.ExecutionCount == nil) != (result.ExecutionCount == nil) || (c.ExecutionCount != nil && *c.ExecutionCount != count) || c.OutputTruncated != result.OutputTruncated {
					t.Fatal("GET content lost result fields", w.Body.String())
				}
			}
		}
		if !found {
			t.Fatal("GET content lost executed cell")
		}
	}
	// Правка с хешем, прочитанным до выполнения; изменения исходника по-прежнему его инвалидируют.
	cellPath := contentPath + "/cells/" + cell.Cell.ID
	w = call("PATCH", cellPath, `{"source":"print(43)"}`, 200, cell.Hash)
	var edited struct {
		Hash string `json:"hash"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &edited); err != nil || edited.Hash == "" || edited.Hash == cell.Hash {
		t.Fatal("source edit did not change hash", err, w.Body.String())
	}
	call("PATCH", cellPath, `{"source":"stale"}`, 409, cell.Hash)

	// Выполнить отредактированный исходник, затем создать с хешем, прочитанным до этого запуска.
	beforeETag = call("GET", contentPath, "", 200).Header().Get("ETag")
	sum = sha256.Sum256([]byte("print(43)"))
	call("POST", base+"/executions", fmt.Sprintf(`{"execution_id":"run-before-create","cell_id":%q,"source_hash":%q}`, cell.Cell.ID, hex.EncodeToString(sum[:])), 202)
	wait(func() bool {
		x, e := store.Execution(ctx, user.ID, session.ID, "run-before-create")
		return e == nil && x.State == "SUCCEEDED" && x.Projection == "applied"
	})
	w = call("GET", contentPath, "", 200)
	if w.Header().Get("ETag") != beforeETag {
		t.Fatal("subsequent execution changed ETag")
	}
	w = call("POST", contentPath+"/cells", `{"kind":"code","source":"print(99)"}`, 201, edited.Hash)
	var added struct {
		Hash string `json:"hash"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &added); err != nil || added.Hash == "" || added.Hash == edited.Hash {
		t.Fatal("cell creation did not change hash", err, w.Body.String())
	}
	call("POST", contentPath+"/cells", `{"kind":"code","source":"stale"}`, 409, edited.Hash)
	saved, err = notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	call("POST", base+"/variables", `{"execution_id":"inspect-1"}`, 202)
	wait(func() bool {
		x, e := store.Execution(ctx, user.ID, session.ID, "inspect-1")
		return e == nil && x.State == "SUCCEEDED"
	})
	w = call("GET", base+"/variables/inspect-1", "", 200)
	if !strings.Contains(w.Body.String(), `"variables":[]`) {
		t.Fatal(w.Body.String())
	}
	call("GET", base+"/executions/inspect-1", "", 404)
	batchReq := D.BatchRequest{RequestID: "batch-all", TimeoutSeconds: 300}
	for _, c := range saved.Cells {
		if c.Kind == "code" {
			hash := sha256.Sum256([]byte(c.Source))
			batchReq.Cells = append(batchReq.Cells, D.BatchCell{CellID: c.ID, SourceHash: hex.EncodeToString(hash[:])})
		}
	}
	batchRaw, err := json.Marshal(batchReq)
	if err != nil {
		t.Fatal(err)
	}
	w = call("POST", base+"/executions/batch", string(batchRaw), 202)
	var batch D.Batch
	if err = json.Unmarshal(w.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	w = call("POST", base+"/executions/batch", string(batchRaw), 202)
	var retry D.Batch
	_ = json.Unmarshal(w.Body.Bytes(), &retry)
	if len(batch.ExecutionIDs) != len(batchReq.Cells) || strings.Join(batch.ExecutionIDs, ",") != strings.Join(retry.ExecutionIDs, ",") {
		t.Fatal("unstable batch response", batch, retry)
	}
	late, err := notebooks.AddCell(ctx, first.ID, user.ID, saved.Hash, "code", "print(99)", nil)
	if err != nil {
		t.Fatal(err)
	}
	w = call("GET", base+"/queue", "", 200)
	var queue D.Queue
	if err = json.Unmarshal(w.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Items) != len(batch.ExecutionIDs) {
		t.Fatal("batch queue not restored", queue)
	}
	for _, item := range queue.Items {
		if item.CellID == late.Cell.ID {
			t.Fatal("late cell entered earlier batch")
		}
	}
	call("POST", base+"/kernel/restart", "", 409)
	wait(func() bool {
		for _, id := range batch.ExecutionIDs {
			x, e := store.Execution(ctx, user.ID, session.ID, id)
			if e != nil || x.State != "SUCCEEDED" || x.Projection != "applied" {
				return false
			}
		}
		return true
	})

	if os.Getenv("RUNTIME_TEST_REAL_AGENT") == "1" {
		snapshot, snapshotErr := notebooks.Content(ctx, first.ID, user.ID)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		for i, source := range []string{"answer = 42", "print(answer)", "print(chr(0))"} {
			added, addErr := notebooks.AddCell(ctx, first.ID, user.ID, snapshot.Hash, "code", source, nil)
			if addErr != nil {
				t.Fatal(addErr)
			}
			hash := sha256.Sum256([]byte(source))
			id := fmt.Sprintf("shared-memory-%d", i)
			call("POST", base+"/executions", fmt.Sprintf(`{"execution_id":%q,"cell_id":%q,"source_hash":%q}`, id, added.Cell.ID, hex.EncodeToString(hash[:])), 202)
			wait(func() bool {
				x, e := store.Execution(ctx, user.ID, session.ID, id)
				return e == nil && x.State == "SUCCEEDED" && ((i < 2 && x.Projection == "applied") || (i == 2 && x.Projection == "conflict"))
			})
			if i == 1 {
				x, e := store.Execution(ctx, user.ID, session.ID, id)
				if e != nil || x.ExecutionCount == nil || len(x.Outputs) == 0 || !strings.Contains(string(x.Outputs[0]), "42") {
					t.Fatal("kernel memory/count not preserved", x, e)
				}
			}
			if i == 2 {
				response := call("GET", base+"/executions/"+id, "", 200)
				var execution D.Execution
				if e := json.Unmarshal(response.Body.Bytes(), &execution); e != nil {
					t.Fatal(e)
				}
				var text strings.Builder
				for _, raw := range execution.Outputs {
					var output struct {
						Text string `json:"text"`
					}
					if e := json.Unmarshal(raw, &output); e != nil {
						t.Fatal(e)
					}
					text.WriteString(output.Text)
				}
				if text.String() != "\x00\n" {
					t.Fatalf("NUL output changed: %q", text.String())
				}
			}
			snapshot, snapshotErr = notebooks.Content(ctx, first.ID, user.ID)
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
		}
		call("POST", base+"/variables", `{"execution_id":"inspect-answer"}`, 202)
		wait(func() bool {
			x, e := store.Execution(ctx, user.ID, session.ID, "inspect-answer")
			return e == nil && x.State == "SUCCEEDED"
		})
		w = call("GET", base+"/variables/inspect-answer", "", 200)
		if !strings.Contains(w.Body.String(), `"name":"answer"`) || !strings.Contains(w.Body.String(), `"preview":"42"`) {
			t.Fatal("variable inspection lost actual value", w.Body.String())
		}
	}
	call("POST", base+"/kernel/interrupt", "", 200)
	call("POST", base+"/kernel/restart", "", 200)
	call("POST", base+"/activity", "", 200)
	pump()
	call("POST", base+"/stop", "", 202)
	call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"while-stopping","language":"python"}`, 409)
	wait(func() bool { v, e := store.Owned(ctx, user.ID, session.ID); return e == nil && v.State == D.Stopped })
	assertBadge(0)
	w = call("GET", fmt.Sprintf("/notebooks/%d/content", first.ID), "", 200)
	if !strings.Contains(w.Body.String(), `43\n`) {
		t.Fatal("results lost after stop", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `\u0000`) {
		t.Fatal("NUL output must remain in the execution archive, not notebook JSONB", w.Body.String())
	}
	// Пользовательская сессия уже завершена, но тест также дожидается реальной очистки тестового окружения.
	wait(func() bool { v, e := store.Owned(ctx, user.ID, session.ID); return e == nil && !v.CleanupError })
	call("GET", "/runtime-sessions/active", "", 200)
	w = call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"second-session","language":"python"}`, 202)
	var rSession struct {
		ID uuid.UUID `json:"id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &rSession); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { v, e := store.Owned(ctx, user.ID, rSession.ID); return e == nil && v.State == D.Ready })
	rContent, rErr := notebooks.Content(ctx, second.ID, user.ID)
	if rErr != nil {
		t.Fatal(rErr)
	}
	rCell, rErr := notebooks.AddCell(ctx, second.ID, user.ID, rContent.Hash, "code", "print(7)", nil)
	if rErr != nil {
		t.Fatal(rErr)
	}
	rHash := sha256.Sum256([]byte("print(7)"))
	rBase := "/runtime-sessions/" + rSession.ID.String()
	call("POST", rBase+"/executions", fmt.Sprintf(`{"execution_id":"r-run","cell_id":%q,"source_hash":%q}`, rCell.Cell.ID, hex.EncodeToString(rHash[:])), 202)
	wait(func() bool {
		x, e := store.Execution(ctx, user.ID, rSession.ID, "r-run")
		return e == nil && x.State == "SUCCEEDED" && x.Projection == "applied"
	})
	// Проверяем независимую правку после выполнения, не меняя исходник print(42) в ранних сценариях.
	renameContent, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	renameCell, err := notebooks.AddCell(ctx, first.ID, user.ID, renameContent.Hash, "markdown", "before", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = notebooks.EditCell(ctx, first.ID, user.ID, renameCell.Hash, renameCell.Cell.ID, "after"); err != nil {
		t.Fatal(err)
	}
	assertRenamed()
	call("POST", "/auth/logout-all", "", 204)
	if w = request("GET", "/runtime-sessions/active", "", "test-bff-key", auth.AccessToken); w.Code != 401 {
		t.Fatal("revoked token accepted", w.Code)
	}
	v, err := store.Owned(ctx, user.ID, rSession.ID)
	if err != nil || v.State != D.Ready {
		t.Fatal("logout-all implicitly stopped VPS", v, err)
	}
	if err = service.Stop(ctx, user.ID, rSession.ID); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { v, e := store.Owned(ctx, user.ID, rSession.ID); return e == nil && v.State == D.Stopped })
}
