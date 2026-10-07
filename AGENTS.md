# Backend: инструкции для агента

Разрабатываем аналог Google Colab: блокноты с блоками Python/R и текста.
Правила работы с задачами, ветками и PR — в [README.md](README.md).
Как завести и закрыть задачу — в [CONTRIBUTING.md](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md) организации;
в Claude Code для этого подключён скилл `cdd-tasks`.

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

В `cmd/` размещай только точки входа в приложение; допустим README с описанием
микросервиса. Тесты в `cmd/` запрещены: проверяемую логику и её тесты размещай
в `internal/` (сборка и запуск API — `internal/app`).

Middleware из [internal/middleware/common.go](internal/middleware/common.go):

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
используй `r.Context()`. Порядок: request ID → logging → recover → CORS →
CSRF/Authenticate → Validator. Общие middleware оборачивают весь роутер, чтобы
request ID был на всех ответах, включая CSRF-отказы, preflight, 404 и 405.

## Целевая архитектура MVP и границы

Одна VPS Selectel с Docker Compose: браузер → Caddy → Go API → PostgreSQL.
Клиент обращается к `/api/v1` на том же домене, BFF нет. Только Caddy публикует
80/443; API и Postgres доступны во внутренней сети Compose. Статика клиента — S3/CDN.

- Backend отвечает за Go API, данные и бизнес-логику; местный Compose — для разработки.
- [Frontend](https://github.com/frontend-park-mail-ru/2026_2_Cringe_Driven_Development):
  клиент React/Vite, обращается к Go API по контракту Apidog.
- `infra`: Pulumi, Ansible Vault, production Compose и Caddyfile. Не переноси
  production-конфигурацию в backend и не коммить открытые секреты.
- Проект Compose на VPS — `cellestial`, каталог `/opt/cellestial`; существующие
  volumes Caddy сохраняются. Миграции goose выполняются при старте API.
- Файлы блокнотов хранятся в S3: нужен `S3_NOTEBOOKS_BUCKET` и параметры `AWS_*`.
  Локально Compose использует SeaweedFS, production — Selectel.
- `ci.yml` проверяет код и публикует образ, затем вызывает `cd.yml` через
  `workflow_call` с SHA-тегом из job `docker`. CD после approval `production`
  запускает `deploy-backend.yml` из `infra/main`. Inventory статический;
  Selectel credentials и Pulumi в CI не используются.
- Reviewers production: `blackHATred`, `YarikMix`; достаточно одного одобрения.
  Деплой только из `main`, без отмены выполняющейся выкатки.
- Откат — тот же playbook с прежним SHA-тегом, без автоматических миграций Down.
- API N+1 обслуживает клиента N: ручки и поля не удалять в том же релизе,
  где клиент перестал ими пользоваться; проверять на ревью Apidog.
- `diagrams/bff/` и `diagrams/frozen-k3s/` в docs заморожены; схемы не изменять.

Источники: [docs](https://github.com/Cringe-Driven-Development-Team/docs),
[MVP-спецификация](https://github.com/Cringe-Driven-Development-Team/docs/blob/main/docs/superpowers/specs/2026-09-29-mvp-single-vps-design.md),
[Deployment](https://cringe-driven-development-team.github.io/docs/deployment.html),
[CD](https://cringe-driven-development-team.github.io/docs/cd.html).

Фактический [CI backend](.github/workflows/ci.yml) публикует
`ghcr.io/go-park-mail-ru/2026_2_cringe_driven_development`: теги `sha-<short>` и `main`,
без `latest`. Пакет приватный; job выкатки передаёт временный `GITHUB_TOKEN`
с `packages: read` через окружение Ansible. Постоянного GHCR-токена в Vault нет.
Выкатывается SHA-тег; строго неизменяемая ссылка — digest.

## Коммиты

Формат: `<тип>: <описание>` или `<тип>(<область>): <описание>`.
Типы: `feat`, `fix`, `refactor`, `style`, `test`, `docs`, `chore`.

```text
docs: добавить инструкции для агента
refactor(auth): вынести проверку токена в middleware
```

Назначение типов — в [README](README.md#типы-коммитов).

## Документация

README содержит краткую, актуальную информацию о проекте и его использовании.
Порядок выкатки конкретной задачи, временные зависимости и условия слияния
описывай в задаче при необходимости, а не в README.
Подробную проектную документацию размещай в репозитории организации
[docs](https://github.com/Cringe-Driven-Development-Team/docs);
локальную папку `docs/` используй по явному запросу.

## Актуальность документа

При изменении команд или архитектурных границ обновляй этот файл в том же изменении, размер этого документа не более 150 строк.
