package cursor

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestCodecRoundTrip(t *testing.T) {
	c := New([]byte("secret"))
	p := usecase.ListPosition{ID: 9007199254740993, UpdatedAt: time.Date(2026, 11, 13, 1, 2, 3, 123456000, time.UTC)}
	token, err := c.Encode(7, "new", p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := New([]byte("secret")).Decode(token, 7, "new")
	if err != nil || got.ID != p.ID || !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("%+v %v", got, err)
	}
	for name, tc := range map[string]struct {
		token  string
		owner  int64
		search string
		key    string
	}{
		"owner": {token, 8, "new", "secret"}, "search": {token, 7, "NEW", "secret"}, "key": {token, 7, "new", "other"},
		"empty": {"", 7, "new", "secret"}, "invalid": {"!.*", 7, "new", "secret"}, "truncated": {token[:len(token)/2], 7, "new", "secret"},
		"payload":   {base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + "." + strings.Split(token, ".")[1], 7, "new", "secret"},
		"signature": {strings.Split(token, ".")[0] + ".YQ", 7, "new", "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New([]byte(tc.key)).Decode(tc.token, tc.owner, tc.search); !errors.Is(err, usecase.ErrInvalidList) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}

func TestCodecRejectsSignedInvalidPayload(t *testing.T) {
	c := New([]byte("secret"))
	for _, body := range []string{
		`{`, `null`, `{}`, `{"v":2,"owner_id":7,"id":1,"updated_at":"2026-11-13T00:00:00Z"}`,
		`{"v":1,"owner_id":7,"id":0,"updated_at":"2026-11-13T00:00:00Z"}`,
		`{"v":1,"owner_id":7,"id":1,"updated_at":"broken"}`,
		`{"v":1,"owner_id":7,"id":9223372036854775808,"updated_at":"2026-11-13T00:00:00Z"}`,
	} {
		token := base64.RawURLEncoding.EncodeToString([]byte(body)) + "." + base64.RawURLEncoding.EncodeToString(c.signature([]byte(body)))
		if _, err := c.Decode(token, 7, ""); !errors.Is(err, usecase.ErrInvalidList) {
			t.Fatalf("accepted %s: %v", body, err)
		}
	}
	for _, position := range []usecase.ListPosition{{}, {ID: -1, UpdatedAt: time.Now()}, {ID: 1}} {
		if _, err := c.Encode(7, "", position); !errors.Is(err, usecase.ErrInvalidList) {
			t.Fatal("accepted invalid position", err)
		}
	}
	if _, err := c.Decode(strings.Repeat("a", 2049), 7, ""); !errors.Is(err, usecase.ErrInvalidList) {
		t.Fatal(err)
	}
}
