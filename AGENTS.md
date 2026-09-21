# Backend: инструкции для агента

Разрабатываем аналог Google Colab: блокноты с блоками Python/R и текста.
Правила работы с задачами, ветками и PR — в [README.md](README.md).

## Целевые продуктовые требования

- Регистрация и авторизация с валидацией; профиль пользователя.
- Список файлов пользователя; страница файла; создание, редактирование и сохранение
  отдельных блоков кода и текста.
- Исполнение файлов на R или Python с выводом результата.
- Шеринг файлов, поиск по файлу, комментарии к блокам кода и текста.
- Выделение квот на количество исполнений и ресурсы; статистика их использования;
  покупка подписок с квотой.
- Загрузка и скачивание ipynb, включая экспорт после исполнения.

## Стек и команды

- Go: версия в [go.mod](go.mod), зависимости через Go Modules (`go.mod`, `go.sum`).
- HTTP: `net/http`, Gorilla mux; PostgreSQL: `pgx/v5/pgxpool`.
- Локально: Docker Compose с Go API и PostgreSQL 18.

Нужны Go, C-компилятор для `-race`, Make, curl и Docker с Compose.
Настройки локального `.env` — в [.env.example](.env.example). Команды из корня:

```bash
go mod download       # скачать зависимости
go mod tidy -diff      # проверить согласованность go.mod/go.sum без изменения
make build            # собрать bin/server
make test             # go test -race -count=1 -coverprofile=coverage.out ./...
make lint             # статический анализ
make docker-build     # собрать colab-backend:local
make run              # собрать и запустить API и PostgreSQL; занимает терминал
```

В другом терминале, при стандартном порте 8080:

```bash
curl --fail http://localhost:8080/health  # {"status": "alive"}
docker compose down                    # остановить локальное окружение
```

## Паттерны кода

Middleware из [cmd/main/main.go](cmd/main/main.go):

```go
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

Сохраняй контекст запроса при передаче дальше; для операций БД из обработчика
используй `r.Context()`. Порядок middleware сейчас: recover → request ID → logging.

## Целевая архитектура MVP и границы

Два VPS Selectel с Docker Compose: браузер → Caddy → BFF (VPS 1) → Go API →
PostgreSQL (VPS 2). BFF обращается к API по приватной сети с S2S-ключом из Vault.
Публичный вход — Caddy; статика клиента — S3/CDN.

- Backend отвечает за Go API, данные и бизнес-логику; местный Compose — для разработки.
- [Frontend](https://github.com/frontend-park-mail-ru/2026_2_Cringe_Driven_Development):
  клиент React/Vite и BFF Hono/bun; bun workspaces и Turborepo. tRPC — между клиентом и BFF.
- `infra`: Pulumi, Ansible, Vault, production Compose и Caddyfile; деплой и откат
  через Ansible. Не переноси production-конфигурацию в backend и не коммить секреты.
- Контракт BFF–Go API, схема данных и механизм исполнения Python/R
  инфраструктурной спецификацией не определены.
- `diagrams/frozen-k3s/` в `docs` заморожен: схемы не изменять, k3s/Argo CD в MVP не переносить.

Источники: [docs](https://github.com/Cringe-Driven-Development-Team/docs),
[MVP-спецификация](https://github.com/Cringe-Driven-Development-Team/docs/blob/main/docs/superpowers/specs/2026-09-16-mvp-compose-design.md),
[каталог схем](https://cringe-driven-development-team.github.io/docs/).

| Схема MVP | Что смотреть |
| --- | --- |
| [Deployment](https://cringe-driven-development-team.github.io/docs/deployment.html) | VPS, маршруты запросов, S3/CDN |
| [Frontend monorepo](https://cringe-driven-development-team.github.io/docs/frontend-monorepo.html) | Клиент, BFF, взаимодействие с Go API |
| [CI](https://cringe-driven-development-team.github.io/docs/ci.html) | Репозитории, проверки, публикация артефактов |
| [CD](https://cringe-driven-development-team.github.io/docs/cd.html) | Деплой и откат через Ansible |

Фактический [CI backend](.github/workflows/ci.yml) публикует
`ghcr.io/go-park-mail-ru/2026_2_cringe_driven_development`: теги `sha-<short>` и `main`,
без `latest`; неизменяемая ссылка — digest.

## Коммиты

Формат: `<тип>: <описание>` или `<тип>(<область>): <описание>`.
Типы: `feat`, `fix`, `refactor`, `style`, `test`, `docs`, `chore`.

```text
docs: добавить инструкции для агента
refactor(auth): вынести проверку токена в middleware
```

Назначение типов — в [README](README.md#типы-коммитов).

## Актуальность документа

При изменении команд или архитектурных границ обновляй этот файл в том же изменении, размер этого документа не более 150 строк.
