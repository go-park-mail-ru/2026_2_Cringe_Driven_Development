package delivery

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// SuggestUsers exposes public identity fields to authenticated callers only.
func (h *Handler) SuggestUsers(ctx context.Context, req api.SuggestUsersRequestObject) (api.SuggestUsersResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return api.SuggestUsers401JSONResponse{Code: api.ErrorCodeUnauthorized, Message: "authentication required"}, nil
	}
	limit := 10
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
		if limit < 1 || limit > 20 {
			return api.SuggestUsers400JSONResponse{Code: api.ErrorCodeValidationError, Message: "invalid suggestion limit"}, nil
		}
	}
	users, err := h.uc.SuggestUsers(ctx, id, req.Params.Q, limit)
	switch {
	case errors.Is(err, usecase.ErrValidation):
		return api.SuggestUsers400JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrRateLimited):
		return api.SuggestUsers429JSONResponse{Code: api.ErrorCodeRateLimited, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	items := make([]api.UserSuggestion, 0, len(users))
	for _, u := range users {
		items = append(items, api.UserSuggestion{Id: u.ID, Login: u.Login})
	}
	return api.SuggestUsers200JSONResponse{Items: items}, nil
}
