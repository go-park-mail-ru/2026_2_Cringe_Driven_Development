package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

var suggestQuery = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

// SuggestUsers uses a shared minute budget and returns public identities only.
func (uc *UserUsecase) SuggestUsers(ctx context.Context, userID int64, q string, limit int) ([]domain.UserSuggestion, error) {
	q = strings.TrimSpace(q)
	if limit == 0 {
		limit = 10
	}
	if userID <= 0 || !suggestQuery.MatchString(q) || limit < 1 || limit > 20 {
		return nil, ErrValidation
	}
	if uc.limiter == nil {
		return nil, errors.New("user suggestions limiter is not configured")
	}
	if err := uc.limiter.Allow(ctx, fmt.Sprintf("user-suggest:%d", userID), 60); err != nil {
		return nil, err
	}
	return uc.users.SuggestUsers(ctx, userID, q, limit)
}
