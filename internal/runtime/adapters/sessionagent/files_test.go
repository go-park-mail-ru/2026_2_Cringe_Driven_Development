package sessionagent

import (
	"context"
	"errors"
	"testing"

	R "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
)

// fileClient проверяет, что служебные credentials доходят только до адаптера.
type fileClient struct {
	Client
	endpoint U.AgentEndpoint
}

// Files запоминает внутренний endpoint без раскрытия его пользователю.
func (c *fileClient) Files(_ context.Context, endpoint U.AgentEndpoint, _ F.FileRequest) (F.FileResponse, error) {
	c.endpoint = endpoint
	return F.FileResponse{Status: 200}, nil
}

// TestFilesGatewayCredentialsAndUnavailable проверяет секреты и недоступную зависимость.
func TestFilesGatewayCredentialsAndUnavailable(t *testing.T) {
	id := uuid.New()
	client := &fileClient{}
	gateway := New(repo{env: R.Environment{SessionID: id, Endpoint: "https://private:8443"}}, secrets{}, client)
	result, err := gateway.Files(context.Background(), id, F.FileRequest{})
	if err != nil || result.Status != 200 || client.endpoint.Token != "provisioned-token" || client.endpoint.BaseURL != "https://private:8443" {
		t.Fatalf("response=%+v endpoint=%+v err=%v", result, client.endpoint, err)
	}
	if _, err = gateway.Files(context.Background(), uuid.New(), F.FileRequest{}); !errors.Is(err, D.ErrNotFound) {
		t.Fatal(err)
	}
	unsupported := New(nil, nil, &unsupportedFiles{})
	if _, err = unsupported.Files(context.Background(), id, F.FileRequest{}); !errors.Is(err, D.ErrUnavailable) {
		t.Fatal(err)
	}
}

// unsupportedFiles представляет старую зависимость без файлового порта.
type unsupportedFiles struct{ Client }
