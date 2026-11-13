// Package cursor signs stateless notebook-list continuation tokens.
package cursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

type Codec struct{ key []byte }
type payload struct {
	Version    int       `json:"v"`
	OwnerID    int64     `json:"owner_id"`
	SearchHash [32]byte  `json:"search_hash"`
	UpdatedAt  time.Time `json:"updated_at"`
	ID         int64     `json:"id"`
}

func New(jwtSecret []byte) *Codec {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte("notebook-list-cursor:v1"))
	return &Codec{key: mac.Sum(nil)}
}
func (c *Codec) signature(data []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write(data)
	return mac.Sum(nil)
}
func (c *Codec) Encode(ownerID int64, search string, position usecase.ListPosition) (string, error) {
	if ownerID <= 0 || position.ID <= 0 || position.UpdatedAt.IsZero() {
		return "", usecase.ErrInvalidList
	}
	data, err := json.Marshal(payload{Version: 1, OwnerID: ownerID, SearchHash: sha256.Sum256([]byte(search)), UpdatedAt: position.UpdatedAt.UTC(), ID: position.ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(c.signature(data)), nil
}
func (c *Codec) Decode(token string, ownerID int64, search string) (usecase.ListPosition, error) {
	invalid := usecase.ErrInvalidList
	// All generated tokens are small and fixed-size except for bounded timestamp text.
	if len(token) > 2048 {
		return usecase.ListPosition{}, invalid
	}
	raw, sig, ok := strings.Cut(token, ".")
	if !ok {
		return usecase.ListPosition{}, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return usecase.ListPosition{}, invalid
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(sig)
	if err != nil || !hmac.Equal(signature, c.signature(data)) {
		return usecase.ListPosition{}, invalid
	}
	var p payload
	if err = json.Unmarshal(data, &p); err != nil || p.Version != 1 || p.OwnerID <= 0 || p.OwnerID != ownerID || p.SearchHash != sha256.Sum256([]byte(search)) || p.ID <= 0 || p.UpdatedAt.IsZero() {
		return usecase.ListPosition{}, invalid
	}
	return usecase.ListPosition{UpdatedAt: p.UpdatedAt, ID: p.ID}, nil
}
