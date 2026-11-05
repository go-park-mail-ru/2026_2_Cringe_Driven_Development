# Локальная разработка backend

Подробности запуска и настройки инструментов для этого репозитория. Короткий старт — в [README](../README.md#быстрый-старт).

## Запуск и проверки

Для локальной работы нужны Go версии из `go.mod`, компилятор C для `-race`,
Make и Docker с Compose. Команды:

```bash
make lint          # статический анализ
make test          # тесты с race detector, coverage.out и итоговое покрытие
make build         # бинарник bin/server
make docker-build  # образ colab-backend:local
make run           # API, PostgreSQL, Redis и SeaweedFS через Docker Compose
```

Первый локальный запуск (существующий `.env` не перезаписывайте):

```bash
cp -n .env.example .env
# Задайте JWT_SECRET и BFF_API_KEY: два разных случайных значения.
# Например, каждое получите командой: openssl rand -hex 32
make doctor
make up           # PostgreSQL + Redis + SeaweedFS + API, health до 120 секунд
make health       # {"status": "alive"}; по умолчанию http://localhost:8080/health
make logs         # последние 100 строк API, БД, Redis и S3
make down         # остановить; volumes с данными сохраняются
```

`make run` запускает то же окружение с логами в текущем терминале. Для другого
worktree задайте отдельный `COMPOSE_PROJECT_NAME` в его `.env` и свободные
`PORT`, `POSTGRES_PORT`, `S3_PORT` (по умолчанию 8080, 5432, 8333).
`POSTGRES_HOST=db` и `AWS_ENDPOINT_URL_S3=http://s3:8333` — внутренние адреса,
их порты при этом не меняются. При отказе `make up` смотрите `make logs` и
`docker compose ps -a`; неверные секреты исправьте в `.env`, затем повторите запуск.
Миграции goose применяются API при старте. `/health` показывает, что API отвечает;
это не проверка всех операций с БД/S3.

## Логи

`LOG_LEVEL` задаёт минимальный уровень JSON-логов: `debug`, `info` (по умолчанию), `warn` или `error`.
По `request_id` можно связать итог HTTP-запроса с событиями usecase и хранилища.
Логи не содержат пароли, токены, тела запросов и содержимое блокнотов.

## Harness AI-агента

Начинайте с корня backend, содержащего `go.mod`, а не общей папки `cdd`.
Codex читает [AGENTS.md](../AGENTS.md); Claude Code загружает его через
[CLAUDE.md](../CLAUDE.md). Карта кода, границы изменений и команды находятся в AGENTS.
Для командного процесса нужен плагин `cdd-tasks` из
[agent-plugins](https://github.com/Cringe-Driven-Development-Team/agent-plugins).
В Claude Code marketplace и плагин объявлены в `.claude/settings.json`;
подтвердите установку в клиенте. В Codex установите плагин из того же репозитория
и убедитесь, что доступен скилл `cdd-tasks`. Перед работой читайте актуальный
[CONTRIBUTING организации](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md):
он имеет приоритет перед скиллом. Правила веток и PR — в [README](../README.md#как-работать-с-задачами).

Установите проверенную версию gopls с поддержкой headless MCP:

```bash
go install golang.org/x/tools/gopls@v0.20.0
export PATH="$(go env GOPATH)/bin:$PATH"  # сохраните в конфигурации своей shell
gopls version                           # v0.20.0
gopls mcp -help
```

В проекте уже есть `.codex/config.toml` для Codex и `.mcp.json` для Claude Code:
оба запускают `gopls mcp` через PATH, без абсолютного пути. Отдельный LSP-клиент
не нужен. Запускайте `codex` или `claude` из этого корня; для worktree — из его
корня. Доверие проекту и разрешение на запуск MCP подтверждаются в клиенте.
Codex загружает проектный config только в trusted-проекте; проверьте
`codex mcp get gopls` и `/mcp` в новой сессии. Проектная запись с тем же именем
перекрывает глобальную: не удаляйте `~/.codex/config.toml` и другие серверы.
В noninteractive `codex exec` запрос подтверждения MCP может завершиться ошибкой
`approval policy is never`. Для автоматической проверки разрешите на один запуск
нужные инструменты через `-c 'mcp_servers.gopls.tools.<tool>.approval_mode="approve"'`,
отдельно для `go_search`, `go_file_context`, `go_symbol_references`, `go_diagnostics`;
`go_workspace` проверяйте первым. Это не требуется для интерактивного подтверждения.
В Claude Code проверьте `claude mcp get gopls`, затем `/mcp`; локальная запись
`gopls` может иметь приоритет над проектной. Личные разрешения не коммитьте.
Источники: [Codex MCP](https://developers.openai.com/codex/mcp/),
[Claude Code MCP](https://code.claude.com/docs/en/mcp).

`make doctor` ничего не устанавливает и не печатает значения секретов. Он
проверяет Go в PATH относительно `go.mod` (с `GOTOOLCHAIN=local`, без
автоматического скачивания toolchain), CGO/C-компилятор для race, Make/curl/Git, gopls MCP,
Docker daemon/Compose, конфигурацию и секреты, авторизацию gh и права записи
в командном `Cringe-Driven-Development-Team/backend`, где теперь находятся и код, и задачи.
Права записи в учебную копию для разработки не нужны. Ошибки собираются вместе, код возврата ненулевой.
Установку клиента, trust, подключение MCP и наличие скилла проверяйте отдельно.
Apidog-токен нужен только для изменения контракта (`make generate`); Projects
нужен для работы с доской (`gh auth refresh -s project`). Сетевой отказ в
песочнице требует разрешения на сеть, а не обязательно нового токена gh.

Проверка в **новой сессии** после установки и принятия trust:

1. Попросите агента прочитать AGENTS.md, назвать точки входа и выполнить `make doctor`.
2. Через MCP вызовите `go_workspace`: ожидается модуль из текущего `go.mod`.
   Если модуль не найден, перезапустите клиент из корня backend.
3. `go_search` с `Handler.GetCurrentUser` должен найти
   `internal/user/delivery/http/user.go`; `go_file_context` — показать зависимости.
   `go_symbol_references` для `NewHandler` в этом файле должен найти сборку в
   `internal/app/app.go` и тесты; `go_diagnostics` для файла — вернуть диагностику.
   В аргументах путей используйте абсолютный путь **своего** checkout.
4. Выполните `make up`, `make health`, `make logs`, затем `make down`.
5. Во время разработки — `make check-fast` и `go test ./internal/<пакет>`;
   перед PR — `make check` и `make docker-build` (соответствуют lint/test/docker CI).
   Docker-проверка не публикует образ. Контракт меняйте по [процессу Apidog](apidog.md).
6. В PR запишите версии клиентов/gopls, результаты MCP, doctor, health и проверок.
   Недоступный клиент отмечайте как непроверенный; запуск MCP сам по себе
   не подтверждает интеграцию с обоими клиентами. Правила ревью и исключение YarikMix — в [README](../README.md#как-работать-с-задачами).

Go API требует `JWT_SECRET`, `BFF_API_KEY`,
`S3_AVATARS_BUCKET` и `S3_AVATARS_PUBLIC_URL`. Запросы к `/api/v1` передают
`X-BFF-Key`, защищённые ручки — также Bearer. Контракт: [авторизация](authentication.md).

## Генерация тестовых моков

Прикладные порты в unit-тестах подменяются `go.uber.org/mock/gomock`, запросы
PostgreSQL — `github.com/pashagolub/pgxmock/v5` без замены pgx/v5 на database/sql.
Версия mockgen закреплена как Go tool в `go.mod`; глобальная установка не нужна.
После `go mod download` выполните из корня backend:

```bash
make generate-mocks
```

Команда запускает только `scripts/generate-mocks.sh`, не обращается к Apidog
и не меняет OpenAPI. Генератор создаёт `*_mock_test.go` рядом с тестами;
при изменении интерфейсов регенерируйте и добавляйте эти файлы вместе с тестами.
Они компилируются только при тестировании и не входят в `coverage.out`.
`make test` считает покрытие рабочего кода через прежний `scripts/test.sh`.

Ожидания SQL используют `QueryMatcherEqual` и точные аргументы, включая владельца.
После теста проверяется `ExpectationsWereMet`. Gomock-контроллер создаётся для
каждого теста/subtest; отсутствие ожиданий запрещает побочные вызовы при отказах.
Stateful fixtures в `flow_fixture_test.go` и `bff_helpers_test.go` сохраняют
последовательные сценарии ротации сессий, создания/изменения/чтения блокнотов
и корреляции логов. Они не заменяют SQL-тесты и не доказывают атомарность PostgreSQL.

## Redis для авторизации

Compose запускает Redis с healthcheck, отдельным volume, AOF и локальным паролем
из `.env.example`. API получает REDIS_*; `/health` проверяет только HTTP.
На каждый защищённый запрос нужен доступ к primary, при сбое возвращается 500.
Настройки и команды тестов — в [runbook Redis](redis-sessions.md).
Для Go API вне Compose нужен Redis, доступный с хоста: задайте REDIS_HOST/PORT
или явно опубликуйте порт только на loopback в личном Compose override.
`make logs` теперь включает Redis. `make down` сохраняет его volume.


## S3 и аватары

Локальный override запускает SeaweedFS 4.48 для аватаров с бакетом
`S3_AVATARS_BUCKET` (`cellestial-avatars-local`). Блокноты хранятся по ячейкам
в PostgreSQL; перенос старых ipynb описан в [notebook-migration.md](notebook-migration.md). Одноразовый сервис `s3-init`
выдаёт `anonymous` только `Read` на бакет аватаров; API ждёт его успешного
завершения. Чтение блокнотов и список объектов остаются закрытыми.
Инициализация повторяется при `make up`, существующие данные сохраняются.
Синтаксис нескольких бакетов и настройки доступа подтверждён в
[mini SeaweedFS 4.48](https://github.com/seaweedfs/seaweedfs/blob/4.48/weed/command/mini.go)
и [s3.bucket.access](https://github.com/seaweedfs/seaweedfs/blob/4.48/weed/shell/command_s3_bucket_access.go).

`AWS_ENDPOINT_URL_S3=http://s3:8333` нужен API внутри Compose.
`S3_AVATARS_PUBLIC_URL` нужен браузеру: локальное значение —
`http://localhost:${S3_PORT:-8333}/${S3_AVATARS_BUCKET:-cellestial-avatars-local}`.
В `.env.example` URL составляется из `S3_PORT` и `S3_AVATARS_BUCKET`;
при работе с другим компьютером укажите доступное ему имя хоста вместо localhost.
Диагностика инициализации: `docker compose logs s3-init`.

При запуске только с `-f docker-compose.yml` задайте действительные AWS_* и оба
бакета, а также публичный URL аватаров самостоятельно. Для production бакет,
CDN и переменные приложения настраивает infra (infra#30); локальный init туда
не переносится. Правила загрузки — в [профиле пользователя](user-profile.md).
