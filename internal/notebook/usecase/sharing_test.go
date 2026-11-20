package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"go.uber.org/mock/gomock"
)

func TestNotebookNameUnicodeLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		err  error
	}{
		{"  имя 😀  ", "имя 😀", nil},
		{strings.Repeat("😀", 128), strings.Repeat("😀", 128), nil},
		{strings.Repeat("я", 129), "", ErrInvalidName},
		{" \t\n ", "", ErrEmptyName},
		{"bad\x00name", "", ErrInvalidName},
		{string([]byte{255}), "", ErrInvalidName},
	} {
		got, err := NormalizeName(tc.name)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Fatalf("normalize %q: %q %v", tc.name, got, err)
		}
	}
}

func TestValidateSharingModes(t *testing.T) {
	for _, tc := range []struct {
		mode   domain.SharingMode
		logins []string
		valid  bool
	}{
		{domain.SharingModePrivate, []string{}, true},
		{domain.SharingModePublic, []string{}, true},
		{domain.SharingModeRestricted, []string{}, true},
		{domain.SharingModeRestricted, []string{"Reader_1", "READER_1"}, true},
		{domain.SharingModePrivate, []string{"reader"}, false},
		{domain.SharingModePublic, []string{"reader"}, false},
		{"unknown", nil, false},
		{domain.SharingModeRestricted, []string{"bad login"}, false},
	} {
		err := ValidateSharing(tc.mode, tc.logins)
		if (err == nil) != tc.valid {
			t.Fatalf("%s %+v: %v", tc.mode, tc.logins, err)
		}
	}
}

func TestCloneValidatesNameAndPassesIdentity(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockRepository(ctrl)
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil)
	repo.EXPECT().Clone(gomock.Any(), int64(42), int64(7), "копия").Return(domain.Notebook{ID: 43, OwnerID: 7, Name: "копия", SharingMode: domain.SharingModePrivate}, nil)
	copy, err := uc.Clone(t.Context(), 42, 7, "  копия  ")
	if err != nil || copy.ID != 43 || !copy.Access.CanManageSharing {
		t.Fatal(copy, err)
	}
	if _, err = uc.Clone(t.Context(), 42, 7, strings.Repeat("я", 129)); !errors.Is(err, ErrInvalidName) {
		t.Fatal(err)
	}
	if _, err = uc.Clone(t.Context(), 42, 0, "name"); err == nil {
		t.Fatal("anonymous clone allowed")
	}
}

func TestCheckReadExposesOnlyPermissionCheck(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockRepository(ctrl)
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil)
	repo.EXPECT().CheckRead(gomock.Any(), int64(42), int64(0)).Return(ErrNotebookNotFound)
	if err := uc.CheckRead(t.Context(), 42, 0); !errors.Is(err, ErrNotebookNotFound) {
		t.Fatal(err)
	}
}
