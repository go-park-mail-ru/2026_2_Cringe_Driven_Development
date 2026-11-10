package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

func newSvc(t *testing.T, enabled bool) (*Service, *MockEnvironmentRepository) {
	ctrl := gomock.NewController(t)
	repo := NewMockEnvironmentRepository(ctrl)
	return NewService(repo, Settings{Enabled: enabled, MaxActive: 3, AgentVersion: "1.2.3", Profiles: map[string]bool{"default": true}}), repo
}

func TestRequestCreateNew(t *testing.T) {
	svc, repo := newSvc(t, true)
	id := uuid.New()
	repo.EXPECT().CreateWithJob(gomock.Any(), gomock.Any(), 3).DoAndReturn(
		func(_ context.Context, e domain.Environment, _ int) (domain.Environment, bool, error) {
			if diff := cmp.Diff(id, e.SessionID); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(domain.StateQueued, e.State); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(domain.StateReady, e.DesiredState); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if e.OperationID == uuid.Nil {
				t.Fatal("operation id is nil")
			}
			if diff := cmp.Diff("1.2.3", e.AgentVersion); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff("default", e.Profile); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			return e, true, nil
		})
	env, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: id, Language: "python"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("python", env.Language); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("default", env.Profile); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func existing(state domain.State, lang string) domain.Environment {
	return domain.Environment{SessionID: uuid.New(), Language: lang, Profile: "default", State: state}
}

func TestRequestCreateSameParamsReturnsExisting(t *testing.T) {
	svc, repo := newSvc(t, true)
	ex := existing(domain.StateCreating, "python")
	repo.EXPECT().CreateWithJob(gomock.Any(), gomock.Any(), 3).Return(ex, false, nil)
	env, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: ex.SessionID, Language: "python", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(ex, env); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestRequestCreateDifferentParamsConflict(t *testing.T) {
	svc, repo := newSvc(t, true)
	ex := existing(domain.StateCreating, "python")
	ex.Profile = "gpu"
	repo.EXPECT().CreateWithJob(gomock.Any(), gomock.Any(), 3).Return(ex, false, nil)
	_, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: ex.SessionID, Language: "python"})
	if gotErr := err; !errors.Is(gotErr, ErrConflict) {
		t.Fatalf("got %v, want %v", gotErr, ErrConflict)
	}
}

func TestRequestCreateFinishedReturnedAsIs(t *testing.T) {
	svc, repo := newSvc(t, true)
	ex := existing(domain.StateDeleted, "python")
	repo.EXPECT().CreateWithJob(gomock.Any(), gomock.Any(), 3).Return(ex, false, nil)
	env, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: ex.SessionID, Language: "python"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(domain.StateDeleted, env.State); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// Поддерживается только python: другие языки, включая r, отклоняются до записи в БД.
func TestRequestCreateInvalidLanguage(t *testing.T) {
	svc, _ := newSvc(t, true)
	for _, lang := range []string{"julia", "r"} {
		_, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: uuid.New(), Language: lang})
		if !errors.Is(err, ErrInvalidLanguage) {
			t.Fatalf("%s: got %v, want %v", lang, err, ErrInvalidLanguage)
		}
	}
}

func TestRequestCreateNilSession(t *testing.T) {
	svc, _ := newSvc(t, true) // repo без ожиданий: вызов CreateWithJob провалит тест
	_, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: uuid.Nil, Language: "python"})
	if gotErr := err; !errors.Is(gotErr, ErrInvalidSession) {
		t.Fatalf("got %v, want %v", gotErr, ErrInvalidSession)
	}
}

func TestRequestCreateUnknownProfile(t *testing.T) {
	svc, _ := newSvc(t, true)
	_, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: uuid.New(), Language: "python", Profile: "big"})
	if gotErr := err; !errors.Is(gotErr, ErrUnknownProfile) {
		t.Fatalf("got %v, want %v", gotErr, ErrUnknownProfile)
	}
}

func TestRequestCreateDisabled(t *testing.T) {
	svc, _ := newSvc(t, false)
	_, err := svc.RequestCreate(context.Background(), CreateEnvironment{SessionID: uuid.New(), Language: "python"})
	if gotErr := err; !errors.Is(gotErr, ErrDisabled) {
		t.Fatalf("got %v, want %v", gotErr, ErrDisabled)
	}
}

func TestRequestDeleteIdempotent(t *testing.T) {
	svc, repo := newSvc(t, true)
	id := uuid.New()
	repo.EXPECT().MarkForDelete(gomock.Any(), id, "why").Return(nil).Times(2)
	if gotErr := svc.RequestDelete(context.Background(), id, "why"); gotErr != nil {
		t.Fatal(gotErr)
	}
	if gotErr := svc.RequestDelete(context.Background(), id, "why"); gotErr != nil {
		t.Fatal(gotErr)
	}
}

func TestRequestDeleteNotFound(t *testing.T) {
	svc, repo := newSvc(t, true)
	id := uuid.New()
	repo.EXPECT().MarkForDelete(gomock.Any(), id, "x").Return(ErrNotFound)
	if gotErr := svc.RequestDelete(context.Background(), id, "x"); !errors.Is(gotErr, ErrNotFound) {
		t.Fatalf("got %v, want %v", gotErr, ErrNotFound)
	}
}
