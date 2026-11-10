package sessionagent

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/fake"
	R "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// repo подставляет заранее подготовленное окружение вместо PostgreSQL в тесте доступа к агенту.
type repo struct {
	U.EnvironmentRepository
	// env Сервис подготовки окружений.
	env R.Environment
}

// Get возвращает подготовленное окружение для проверки подключения к агенту.
func (r repo) Get(_ context.Context, id uuid.UUID) (R.Environment, error) {
	if id != r.env.SessionID {
		return R.Environment{}, U.ErrNotFound
	}
	return r.env, nil
}

// LoadSecrets возвращает подготовленные тестовые секреты окружения.
func (r repo) LoadSecrets(context.Context, uuid.UUID) (U.SealedSecrets, error) {
	return U.SealedSecrets{Token: []byte("provisioned-token")}, nil
}

// secrets возвращает тестовый токен без реального шифрования для проверки передачи учётных данных.
type secrets struct{ U.Secrets }

// Open возвращает тестовый токен без реальной расшифровки.
func (secrets) Open(b []byte) ([]byte, error) { return b, nil }

// TestGatewayUsesProvisionedCredential проверяет, что клиент использует выданные окружению адрес и токен, а
// повтор команды не меняет код.
func TestGatewayUsesProvisionedCredential(t *testing.T) {
	id := uuid.New()
	a := fake.NewAgent()
	a.Register("https://private:8443", "provisioned-token", id, "python")
	g := New(repo{env: R.Environment{SessionID: id, Endpoint: "https://private:8443"}}, secrets{}, a)
	h, err := g.Health(context.Background(), id)
	if err != nil || h.SessionID != id || h.Language != "python" {
		t.Fatal(h, err)
	}
	cmd := D.Command{ExecutionID: "run", Code: "print(42)", TimeoutSeconds: 17}
	x, err := g.Execute(context.Background(), id, cmd)
	if err != nil || x.State != "SUCCEEDED" {
		t.Fatal(x, err)
	}
	cmd.Code = "print(43)"
	if _, err = g.Execute(context.Background(), id, cmd); !errors.Is(err, D.ErrConflict) {
		t.Fatal("duplicate changed code admitted", err)
	}
	events, err := g.Events(context.Background(), id, 0, D.EventLimits{})
	if err != nil || len(events) == 0 {
		t.Fatal(events, err)
	}
}
