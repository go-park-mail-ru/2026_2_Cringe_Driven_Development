package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func testID() uuid.UUID { return uuid.MustParse("11111111-1111-1111-1111-111111111111") }

func newPool(t *testing.T) pgxmock.PgxPoolIface { //nolint:ireturn // pgxmock отдаёт интерфейс
	t.Helper()
	db, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return db
}

func testEnv() domain.Environment {
	return domain.Environment{SessionID: testID(), OperationID: uuid.New(), Language: "python", Profile: "default",
		State: domain.StateQueued, DesiredState: domain.StateReady, AgentVersion: "1.0", Version: 1}
}

func envCols() []string {
	return []string{"session_id", "operation_id", "language", "profile", "state", "desired_state", "server_id", "volume_id",
		"port_id", "private_ip", "endpoint", "agent_version", "last_error", "cleanup_result", "pool_id", "version", "created_at", "updated_at"}
}

func envRow(e domain.Environment) *pgxmock.Rows {
	return pgxmock.NewRows(envCols()).AddRow(e.SessionID, e.OperationID, e.Language, e.Profile, string(e.State), string(e.DesiredState),
		e.Resources.ServerID, e.Resources.VolumeID, e.Resources.PortID, e.Resources.PrivateIP, e.Endpoint, e.AgentVersion,
		e.LastError, e.CleanupResult, e.PoolID, e.Version, e.CreatedAt, e.UpdatedAt)
}

func insertEnvArgs(e domain.Environment) []any {
	return []any{e.SessionID, e.OperationID, e.Language, e.Profile, string(e.State), string(e.DesiredState), e.AgentVersion}
}

func TestCreateWithJobInsertsBothInOneTx(t *testing.T) {
	env := testEnv()
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectExec(capacityLockSQL).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnError(pgx.ErrNoRows)
	db.ExpectQuery(countActiveSQL).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	db.ExpectQuery(insertEnvSQL).WithArgs(insertEnvArgs(env)...).WillReturnRows(pgxmock.NewRows([]string{"version", "created_at", "updated_at"}).AddRow(int64(1), time.Now(), time.Now()))
	db.ExpectExec(insertJobSQL).WithArgs(testID(), "create").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	db.ExpectCommit()
	db.ExpectRollback()
	got, created, err := NewRepository(db).CreateWithJob(context.Background(), env, 3)
	if err != nil || !created || got.SessionID != testID() {
		t.Fatalf("got=%+v created=%v err=%v", got, created, err)
	}
}

func TestCreateWithJobExistingReturnsIt(t *testing.T) {
	existing := testEnv()
	existing.State = domain.StateReady
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectExec(capacityLockSQL).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnRows(envRow(existing))
	db.ExpectRollback()
	got, created, err := NewRepository(db).CreateWithJob(context.Background(), testEnv(), 3)
	if err != nil || created || got.State != domain.StateReady {
		t.Fatalf("got=%+v created=%v err=%v", got, created, err)
	}
}

func TestCreateWithJobConflictFallsBackToGet(t *testing.T) {
	env := testEnv()
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectExec(capacityLockSQL).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnError(pgx.ErrNoRows)
	db.ExpectQuery(countActiveSQL).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	db.ExpectQuery(insertEnvSQL).WithArgs(insertEnvArgs(env)...).WillReturnError(pgx.ErrNoRows)
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnRows(envRow(env))
	db.ExpectRollback()
	_, created, err := NewRepository(db).CreateWithJob(context.Background(), env, 3)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestCreateWithJobCapacity(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectExec(capacityLockSQL).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnError(pgx.ErrNoRows)
	db.ExpectQuery(countActiveSQL).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	db.ExpectRollback()
	_, _, err := NewRepository(db).CreateWithJob(context.Background(), testEnv(), 3)
	if !errors.Is(err, usecase.ErrCapacity) {
		t.Fatalf("err=%v", err)
	}
}

func TestMarkForDeleteConvertsActiveJob(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(lockEnvSQL).WithArgs(testID()).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("CREATING"))
	db.ExpectExec(markDeleteSQL).WithArgs(testID(), "user").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	db.ExpectExec(convertJobSQL).WithArgs(testID()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	db.ExpectCommit()
	db.ExpectRollback()
	if err := NewRepository(db).MarkForDelete(context.Background(), testID(), "user"); err != nil {
		t.Fatal(err)
	}
}

func TestMarkForDeleteInsertsWhenNoActiveJob(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(lockEnvSQL).WithArgs(testID()).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("READY"))
	db.ExpectExec(markDeleteSQL).WithArgs(testID(), "user").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	db.ExpectExec(convertJobSQL).WithArgs(testID()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	db.ExpectExec(insertJobSQL).WithArgs(testID(), "delete").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	db.ExpectCommit()
	db.ExpectRollback()
	if err := NewRepository(db).MarkForDelete(context.Background(), testID(), "user"); err != nil {
		t.Fatal(err)
	}
}

func TestMarkForDeleteFinishedNoopAndNotFound(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(lockEnvSQL).WithArgs(testID()).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("DELETED"))
	db.ExpectCommit()
	db.ExpectRollback()
	db.ExpectBegin()
	db.ExpectQuery(lockEnvSQL).WithArgs(testID()).WillReturnError(pgx.ErrNoRows)
	db.ExpectRollback()
	r := NewRepository(db)
	if err := r.MarkForDelete(context.Background(), testID(), "x"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkForDelete(context.Background(), testID(), "x"); !errors.Is(err, usecase.ErrNotFound) {
		t.Fatal(err)
	}
}

func saveArgs(e domain.Environment, owner string) []any {
	return []any{e.SessionID, e.Version, string(e.State), string(e.DesiredState), e.Resources.ServerID, e.Resources.VolumeID,
		e.Resources.PortID, e.Resources.PrivateIP, e.Endpoint, e.AgentVersion, e.LastError, e.CleanupResult, owner, e.PoolID}
}

func TestSaveStaleVersion(t *testing.T) {
	env := testEnv()
	db := newPool(t)
	db.ExpectQuery(saveSQL).WithArgs(saveArgs(env, "w1")...).WillReturnError(pgx.ErrNoRows)
	_, err := NewRepository(db).Save(context.Background(), env, "w1")
	if !errors.Is(err, usecase.ErrStale) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveBumpsVersion(t *testing.T) {
	env := testEnv()
	now := time.Now()
	db := newPool(t)
	db.ExpectQuery(saveSQL).WithArgs(saveArgs(env, "w1")...).WillReturnRows(pgxmock.NewRows([]string{"version", "updated_at"}).AddRow(int64(2), now))
	got, err := NewRepository(db).Save(context.Background(), env, "w1")
	if err != nil || got.Version != 2 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestSaveSecretsStaleAndLoad(t *testing.T) {
	s := usecase.SealedSecrets{Token: []byte("t"), HostPrivateKey: []byte("k"), HostPublicKey: "pub"}
	db := newPool(t)
	db.ExpectExec(saveSecretsSQL).WithArgs(testID(), s.Token, s.HostPrivateKey, s.HostPublicKey, "w1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	db.ExpectQuery(loadSecretsSQL).WithArgs(testID()).WillReturnRows(pgxmock.NewRows([]string{"a", "b", "c"}).AddRow(s.Token, s.HostPrivateKey, "pub"))
	r := NewRepository(db)
	if err := r.SaveSecrets(context.Background(), testID(), s, "w1"); !errors.Is(err, usecase.ErrStale) {
		t.Fatal(err)
	}
	got, err := r.LoadSecrets(context.Background(), testID())
	if err != nil || !bytes.Equal(got.Token, s.Token) || got.HostPublicKey != "pub" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestKnownSessions(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(knownSQL).WithArgs([]uuid.UUID{testID()}).WillReturnRows(pgxmock.NewRows([]string{"id", "state"}).AddRow(testID(), "READY"))
	m, err := NewRepository(db).KnownSessions(context.Background(), []uuid.UUID{testID()})
	if err != nil || m[testID()] != domain.StateReady {
		t.Fatalf("m=%v err=%v", m, err)
	}
}

func TestListByState(t *testing.T) {
	env := testEnv()
	env.State = domain.StateCleanupPending
	db := newPool(t)
	db.ExpectQuery(listByStateSQL).WithArgs("CLEANUP_PENDING").WillReturnRows(envRow(env))
	got, err := NewRepository(db).ListByState(context.Background(), domain.StateCleanupPending)
	if err != nil || len(got) != 1 || got[0].SessionID != env.SessionID || got[0].State != domain.StateCleanupPending {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestListByStateQueryError(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(listByStateSQL).WithArgs("CLEANUP_PENDING").WillReturnError(errors.New("boom"))
	if _, err := NewRepository(db).ListByState(context.Background(), domain.StateCleanupPending); err == nil {
		t.Fatal("want error")
	}
}

func TestGetNotFound(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(selectEnvSQL).WithArgs(testID()).WillReturnError(pgx.ErrNoRows)
	if _, err := NewRepository(db).Get(context.Background(), testID()); !errors.Is(err, usecase.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestSecretsNeverLoggedInErrors(t *testing.T) {
	secret := []byte("SECRET-TOKEN-BYTES")
	s := usecase.SealedSecrets{Token: secret, HostPrivateKey: secret, HostPublicKey: "pub"}
	db := newPool(t)
	db.ExpectExec(saveSecretsSQL).WithArgs(testID(), s.Token, s.HostPrivateKey, s.HostPublicKey, "w1").WillReturnError(errors.New("boom"))
	err := NewRepository(db).SaveSecrets(context.Background(), testID(), s, "w1")
	if err == nil || strings.Contains(err.Error(), string(secret)) {
		t.Fatalf("err=%v", err)
	}
}

func TestMarkForDeleteBumpsVersion(t *testing.T) {
	if !strings.Contains(markDeleteSQL, "version = version + 1") {
		t.Fatal("markDelete must bump version")
	}
}

func TestSaveNeverClearsDeleteFlag(t *testing.T) {
	if !strings.Contains(saveSQL, "CASE WHEN e.desired_state = 'DELETED' THEN 'DELETED' ELSE $4 END") {
		t.Fatal("save must keep desired_state=DELETED")
	}
}

func TestSaveStaleAfterMarkForDelete(t *testing.T) {
	env := testEnv() // версия до MarkForDelete
	db := newPool(t)
	db.ExpectQuery(saveSQL).WithArgs(saveArgs(env, "w1")...).WillReturnError(pgx.ErrNoRows)
	if _, err := NewRepository(db).Save(context.Background(), env, "w1"); !errors.Is(err, usecase.ErrStale) {
		t.Fatalf("err=%v", err)
	}
}
