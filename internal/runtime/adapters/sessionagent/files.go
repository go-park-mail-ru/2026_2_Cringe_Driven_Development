package sessionagent

import (
	"context"

	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
)

// Files раскрывает credentials только внутреннему файловому клиенту.
func (g *Gateway) Files(ctx context.Context, id uuid.UUID, req F.FileRequest) (F.FileResponse, error) {
	client, ok := g.client.(interface {
		Files(context.Context, U.AgentEndpoint, F.FileRequest) (F.FileResponse, error)
	})
	if !ok {
		return F.FileResponse{}, D.ErrUnavailable
	}
	endpoint, err := g.endpoint(ctx, id)
	if err != nil {
		return F.FileResponse{}, err
	}
	return client.Files(ctx, endpoint, req)
}
