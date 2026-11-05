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
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	nbpg "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	nbs3 "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/s3"
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
	dsn, redisAddr, s3Endpoint := os.Getenv("RUNTIME_TEST_DATABASE_URL"), os.Getenv("REDIS_TEST_ADDR"), os.Getenv("RUNTIME_TEST_S3_ENDPOINT")
	if dsn == "" || redisAddr == "" || s3Endpoint == "" {
		t.Skip("set RUNTIME_TEST_DATABASE_URL, REDIS_TEST_ADDR, RUNTIME_TEST_S3_ENDPOINT for isolated integration")
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
	s3 := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("local-test", "local-test", "")}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(s3Endpoint); o.UsePathStyle = true })
	bucket := strings.ReplaceAll(name, "_", "-")
	if _, err = s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		out, e := s3.ListObjectsV2(context.Background(), &awss3.ListObjectsV2Input{Bucket: &bucket})
		if e == nil {
			for _, o := range out.Contents {
				_, _ = s3.DeleteObject(context.Background(), &awss3.DeleteObjectInput{Bucket: &bucket, Key: o.Key})
			}
		}
		_, _ = s3.DeleteBucket(context.Background(), &awss3.DeleteBucketInput{Bucket: &bucket})
	}()
	files := nbs3.NewFileStorage(s3, bucket)
	writer := ipynb.RuntimeFiles{Files: files}
	issuer := security.NewAccessToken([]byte("api13-integration-local-key"), time.Hour)
	users := useruc.NewUserUsecase(userpg.NewUserRepository(pool), sessions, issuer, security.PasswordHasher{}, security.RefreshTokens{}, time.Hour)
	notebooks := nbuc.NewNotebookUsecase(nbpg.NewNotebookRepository(pool), files, ipynb.Codec{})
	module, err := runtime.Build(ctx, config.RuntimeConfig{Enabled: true, Provider: "fake", SecretsKey: make([]byte, 32), MaxActive: 5, MaxConcurrentOps: 2, StartupTimeout: time.Minute, WorkDir: t.TempDir()}, pool)
	if err != nil {
		t.Fatal(err)
	}
	store := storepg.New(pool, writer)
	sessionAgent := module.Agent
	if os.Getenv("RUNTIME_TEST_REAL_AGENT") == "1" {
		sessionAgent = newRealSessionAgent(t, func(ctx context.Context, id uuid.UUID) (string, error) {
			var language string
			queryErr := pool.QueryRow(ctx, "SELECT language FROM runtime_sessions WHERE id=$1", id).Scan(&language)
			return language, queryErr
		})
	}
	service := UC.NewService(store, UC.Settings{Enabled: true}, nil, sessionAgent)
	worker := UC.NewWorker(store, env.New(module.Environments), sessionAgent, writer, nil)
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
	request := func(method, path, body, key, token string) *httptest.ResponseRecorder {
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
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(method, path, body, "test-bff-key", auth.AccessToken)
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
	w = call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"start-second","language":"r"}`, 409)
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
		if e = store.Project(ctx, writer); e != nil && !errors.Is(e, D.ErrNotFound) {
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
	base := "/runtime-sessions/" + session.ID.String()
	sum := sha256.Sum256([]byte("print(42)"))
	executeBody := fmt.Sprintf(`{"execution_id":"run","cell_id":%q,"source_hash":%q}`, cell.Cell.ID, hex.EncodeToString(sum[:]))
	call("POST", base+"/executions", executeBody, 202)
	wait(func() bool {
		x, e := store.Execution(ctx, user.ID, session.ID, "run")
		return e == nil && x.State == "SUCCEEDED" && x.Projection == "applied"
	})
	w = call("GET", base+"/executions/run", "", 200)
	if !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", base+"/events?after=0", "", 200)
	if !strings.Contains(w.Body.String(), `"next_cursor"`) || !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal(w.Body.String())
	}
	var fileKey string
	if err = pool.QueryRow(ctx, "SELECT file_key FROM notebooks WHERE id=$1", first.ID).Scan(&fileKey); err != nil {
		t.Fatal(err)
	}
	raw, err := files.Get(ctx, fileKey)
	if err != nil || !strings.Contains(string(raw), `42\n`) {
		t.Fatal(string(raw), err)
	}

	saved, err := notebooks.Content(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Hash != cell.Hash {
		t.Fatal("output changed the editor hash")
	}
	w = call("GET", fmt.Sprintf("/notebooks/%d/content", first.ID), "", 200)
	if !strings.Contains(w.Body.String(), `"output_truncated":false`) || !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal("saved content result missing", w.Body.String())
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
		for i, source := range []string{"answer = 42", "print(answer)"} {
			added, addErr := notebooks.AddCell(ctx, first.ID, user.ID, snapshot.Hash, "code", source, nil)
			if addErr != nil {
				t.Fatal(addErr)
			}
			hash := sha256.Sum256([]byte(source))
			id := fmt.Sprintf("shared-memory-%d", i)
			call("POST", base+"/executions", fmt.Sprintf(`{"execution_id":%q,"cell_id":%q,"source_hash":%q}`, id, added.Cell.ID, hex.EncodeToString(hash[:])), 202)
			wait(func() bool {
				x, e := store.Execution(ctx, user.ID, session.ID, id)
				return e == nil && x.State == "SUCCEEDED" && x.Projection == "applied"
			})
			if i == 1 {
				x, e := store.Execution(ctx, user.ID, session.ID, id)
				if e != nil || x.ExecutionCount == nil || len(x.Outputs) == 0 || !strings.Contains(string(x.Outputs[0]), "42") {
					t.Fatal("kernel memory/count not preserved", x, e)
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
	call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"while-stopping","language":"r"}`, 409)
	wait(func() bool { v, e := store.Owned(ctx, user.ID, session.ID); return e == nil && v.State == D.Stopped })
	w = call("GET", fmt.Sprintf("/notebooks/%d/content", first.ID), "", 200)
	if !strings.Contains(w.Body.String(), `42\n`) {
		t.Fatal("results lost after stop", w.Body.String())
	}
	call("GET", "/runtime-sessions/active", "", 200)
	w = call("POST", fmt.Sprintf("/notebooks/%d/runtime-sessions", second.ID), `{"request_id":"r-session","language":"r"}`, 202)
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
