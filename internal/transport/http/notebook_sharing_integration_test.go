package httptransport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	notebookdb "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotebookSharingHTTPPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "sharing_http_" + uuid.NewString()
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if dropErr != nil {
			t.Error(dropErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(1,'owner','hash'),(2,'reader','hash'),(3,'other','hash')`); err != nil {
		t.Fatal(err)
	}
	repo := notebookdb.NewNotebookRepository(pool)
	uc := notebookusecase.NewNotebookUsecase(repo, notebookusecase.NoActiveSessions{}, notebookcursor.New([]byte("sharing-http-cursor")))
	n, err := uc.Create(ctx, 1, "Original")
	if err != nil {
		t.Fatal(err)
	}
	issuer := usersecurity.NewAccessToken([]byte("sharing-http-token-key"), time.Hour)
	tokens := map[int64]string{}
	for _, id := range []int64{1, 2, 3} {
		tokens[id], err = issuer.Issue(testClaims(id))
		if err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewRouter(Server{NotebookHandler: notebookdelivery.NewHandler(uc)}, issuer, allowSessions{}, "sharing-http-bff")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, user int64, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, BaseURL+path, strings.NewReader(body))
		req.Header.Set("X-BFF-Key", "sharing-http-bff")
		if user != 0 {
			req.Header.Set("Authorization", "Bearer "+tokens[user])
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s user=%d: status=%d want=%d body=%s", method, path, user, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	path := fmt.Sprintf("/notebooks/%d", n.ID)
	request(http.MethodPut, path+"/sharing", `{"mode":"restricted","reader_logins":["READER"]}`, 1, 200)
	for _, suffix := range []string{"", "/content"} {
		rec := request(http.MethodGet, path+suffix, "", 2, 200)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("reader response is cacheable")
		}
		request(http.MethodGet, path+suffix, "", 3, 404)
		request(http.MethodGet, path+suffix, "", 0, 404)
	}
	request(http.MethodGet, path+"/sharing", "", 2, 404)
	request(http.MethodPatch, path, `{"name":"Forbidden"}`, 2, 404)
	cloneResponse := request(http.MethodPost, path+"/clone", `{"name":" My copy "}`, 2, 201)
	var copyBody struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Access struct {
			Owner bool `json:"is_owner"`
			Edit  bool `json:"can_edit"`
		} `json:"access"`
	}
	if err = json.Unmarshal(cloneResponse.Body.Bytes(), &copyBody); err != nil {
		t.Fatal(err)
	}
	if copyBody.ID == n.ID || copyBody.ID <= 0 || copyBody.Name != "My copy" || !copyBody.Access.Owner || !copyBody.Access.Edit {
		t.Fatalf("invalid clone: %+v", copyBody)
	}
	copyPath := fmt.Sprintf("/notebooks/%d", copyBody.ID)
	request(http.MethodGet, copyPath, "", 2, 200)
	sharingResponse := request(http.MethodGet, copyPath+"/sharing", "", 2, 200)
	if !strings.Contains(sharingResponse.Body.String(), `"mode":"private"`) {
		t.Fatal(sharingResponse.Body.String())
	}
	listResponse := request(http.MethodGet, "/notebooks", "", 2, 200)
	if !strings.Contains(listResponse.Body.String(), `"name":"My copy"`) {
		t.Fatal(listResponse.Body.String())
	}
	request(http.MethodPut, path+"/sharing", `{"mode":"public","reader_logins":[]}`, 1, 200)
	for _, suffix := range []string{"", "/content"} {
		request(http.MethodGet, path+suffix, "", 0, 200)
	}
	request(http.MethodPost, path+"/clone", `{"name":"Anonymous copy"}`, 0, 401)
	tokens[99] = "invalid-token"
	request(http.MethodGet, path, "", 99, 401)
	noBFF := httptest.NewRequest(http.MethodGet, BaseURL+path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, noBFF)
	if rec.Code != 403 {
		t.Fatalf("missing BFF: got %d", rec.Code)
	}
	request(http.MethodPut, path+"/sharing", `{"mode":"private","reader_logins":[]}`, 1, 200)
	for _, suffix := range []string{"", "/content"} {
		request(http.MethodGet, path+suffix, "", 0, 404)
		request(http.MethodGet, path+suffix, "", 2, 404)
	}
	request(http.MethodDelete, path, "", 1, 204)
	request(http.MethodGet, copyPath, "", 2, 200)
}
