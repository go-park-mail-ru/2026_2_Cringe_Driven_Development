// Package redis хранит сессии авторизации на отдельном primary Redis.
package redis

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	goredis "github.com/redis/go-redis/v9"

	_ "embed"
)

//go:embed session.lua
var sessionScript string

// Store не повторяет мутации: таймаут мог случиться уже после записи.
// Вызывающий также обязан отключить ретраи команд go-redis на переданном клиенте.
type Store struct {
	client  *goredis.Client
	prefix  string
	timeout time.Duration
}

var _ usecase.SessionStore = (*Store)(nil)

func NewStore(client *goredis.Client, prefix string, timeout time.Duration) *Store {
	return &Store{client: client, prefix: prefix, timeout: timeout}
}
func (s *Store) authKey(sid string) string     { return s.prefix + "auth_session:" + sid }
func (s *Store) refreshKey(hash string) string { return s.prefix + "refresh_session:" + hash }
func (s *Store) versionKey(id int64) string {
	return s.prefix + "user_session_version:" + strconv.FormatInt(id, 10)
}

type record struct {
	UserID           string `json:"user_id,omitempty"`
	SID              string `json:"sid,omitempty"`
	Version          string `json:"version,omitempty"`
	TokenHash        string `json:"token_hash,omitempty"`
	RefreshExpiresAt int64  `json:"refresh_expires_at,omitempty"`
	AccessValidUntil int64  `json:"access_valid_until,omitempty"`
	OldHash          string `json:"old_hash,omitempty"`
}

func claimsRecord(c usecase.SessionClaims) record {
	return record{UserID: strconv.FormatInt(c.UserID, 10), SID: c.SessionID, Version: strconv.FormatUint(uint64(c.Version), 10)}
}
func validClaims(c usecase.SessionClaims) bool {
	return c.UserID > 0 && c.SessionID != "" && c.Version > 0
}
func sessionRecord(n usecase.NewSession) record {
	r := claimsRecord(n.SessionClaims)
	r.TokenHash = n.TokenHash
	// Округляем дедлайн хранилища вверх, чтобы не укоротить срок действия токена.
	r.RefreshExpiresAt = n.ExpiresAt.Add(time.Millisecond - 1).UnixMilli()
	r.AccessValidUntil = n.AccessExpiresAt.Add(time.Millisecond - 1).UnixMilli()
	return r
}
func (s *Store) run(ctx context.Context, op string, r record, keys ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	// EVAL избегает лишнего раунда EVALSHA/NOSCRIPT и любой неоднозначности ретраев.
	out, err := s.client.Eval(ctx, sessionScript, keys, op, string(b), s.prefix).Text()
	if err != nil {
		return "", fmt.Errorf("redis session %s: %w", op, err)
	}
	switch out {
	case "invalid":
		return "", usecase.ErrInvalidSession
	case "version":
		return "", usecase.ErrVersionChanged
	}
	return out, nil
}
func candidate() (string, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64((1<<62)-1))
	if err != nil {
		return "", err
	}
	return n.Add(n, big.NewInt(1)).String(), nil
}
func (s *Store) CurrentVersion(ctx context.Context, id int64) (usecase.SessionVersion, error) {
	if id <= 0 {
		return 0, errors.New("invalid user ID")
	}
	v, err := candidate()
	if err != nil {
		return 0, err
	}
	out, err := s.run(ctx, "current", record{Version: v}, s.versionKey(id))
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(out, 10, 64)
	return usecase.SessionVersion(n), err
}
func (s *Store) Create(ctx context.Context, n usecase.NewSession) error {
	if !validClaims(n.SessionClaims) || n.TokenHash == "" {
		return usecase.ErrInvalidSession
	}
	_, err := s.run(ctx, "create", sessionRecord(n), s.versionKey(n.UserID), s.authKey(n.SessionID), s.refreshKey(n.TokenHash))
	return err
}
func (s *Store) Find(ctx context.Context, hash string) (usecase.RefreshSession, error) {
	out, err := s.run(ctx, "find", record{OldHash: hash}, s.refreshKey(hash))
	if err != nil {
		return usecase.RefreshSession{}, err
	}
	var r record
	if err = json.Unmarshal([]byte(out), &r); err != nil {
		return usecase.RefreshSession{}, fmt.Errorf("decode refresh session: %w", err)
	}
	id, err := strconv.ParseInt(r.UserID, 10, 64)
	if err != nil {
		return usecase.RefreshSession{}, err
	}
	v, err := strconv.ParseUint(r.Version, 10, 64)
	if err != nil {
		return usecase.RefreshSession{}, err
	}
	return usecase.RefreshSession{SessionClaims: usecase.SessionClaims{UserID: id, SessionID: r.SID, Version: usecase.SessionVersion(v)}, TokenHash: r.TokenHash, ExpiresAt: time.UnixMilli(r.RefreshExpiresAt)}, nil
}
func (s *Store) ValidateAccess(ctx context.Context, c usecase.SessionClaims) error {
	if !validClaims(c) {
		return usecase.ErrInvalidSession
	}
	_, err := s.run(ctx, "access", claimsRecord(c), s.authKey(c.SessionID))
	return err
}
func (s *Store) Rotate(ctx context.Context, old string, n usecase.NewSession) error {
	if !validClaims(n.SessionClaims) || n.TokenHash == "" {
		return usecase.ErrInvalidSession
	}
	r := sessionRecord(n)
	r.OldHash = old
	_, err := s.run(ctx, "rotate", r, s.refreshKey(old))
	return err
}
func (s *Store) Revoke(ctx context.Context, hash string, c usecase.SessionClaims) error {
	if !validClaims(c) {
		return usecase.ErrInvalidSession
	}
	r := claimsRecord(c)
	r.OldHash = hash
	_, err := s.run(ctx, "revoke", r, s.refreshKey(hash))
	return err
}
func (s *Store) RevokeAll(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid user ID")
	}
	v, err := candidate()
	if err != nil {
		return err
	}
	_, err = s.run(ctx, "all", record{Version: v}, s.versionKey(id))
	return err
}
