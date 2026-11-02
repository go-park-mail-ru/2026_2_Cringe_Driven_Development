# Backend: инструкции для агента

Разрабатываем аналог Google Colab: блокноты с блоками Python/R и текста. Правила работы с задачами, ветками и PR — в [README.md](README.md). Как завести и закрыть задачу — в [CONTRIBUTING.md](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md) организации; в Codex и Claude Code используй командный скилл `cdd-tasks`. Код/PR/issues — Cringe-Driven-Development-Team/backend; api-N, `Closes #N`, review ментора, кроме настроенного исключения YarikMix (см. README). Учебный repo — копия main без Actions и новых PR.

## Wiki и общий поиск

При конкретном вопросе о Go API начни с собственной `openwiki/quickstart.md`; не читай wiki в начале каждой задачи.
Корень checkout: `git rev-parse --show-toplevel`; проверь `origin`: `Cringe-Driven-Development-Team/backend`. Перед использованием: `bun scripts/openwiki-check.ts --against <полный актуальный SHA backend/main>`.
При stale wiki — только указатель; утверждения сверь с актуальным кодом и Apidog. Без wiki читай README, docs и исходники.
Для межрепозиторных вопросов используй общий workspace соседних `docs`, `frontend`, `backend`, `infra`: настройка — в README docs. При нескольких пространствах проверь `openwiki workspace current`, выбери через `openwiki workspace use`. В MCP используй `openwiki_search`, затем `openwiki_read` с ID wiki из результата; не считай чужую страницу локальной.
Карта владения — docs, клиент/BFF — frontend, выкладка — infra; внутренности `@cdd-team/*` — frontend-packages по карте docs.
Без MCP читай Markdown quickstart нужного клона или GitHub: https://github.com/Cringe-Driven-Development-Team/backend/tree/main/openwiki. Соседний docs из worktree: `git -C "$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")/../docs" rev-parse --show-toplevel`.
Проверь его `origin`: `Cringe-Driven-Development-Team/docs`; без клона — https://github.com/Cringe-Driven-Development-Team/docs/tree/main/openwiki.
В docs проверяй `bun run wiki:check --against <полный актуальный SHA docs/main>`, в платформах — их `bun scripts/openwiki-check.ts --against <SHA main>`.
Пути определяй заново. SHA одной wiki не подтверждает соседний код; состояние прода проверяй по стенду, будущие фичи — в `/modules/` docs.

## Учебный проект: совместимость и прод

Живых пользователей нет; прод проверяют примерно раз в месяц. Временный простой между выкладками допустим.
Без прямого требования владельца не добавляй в задачи и код legacy, старый API, поэтапный деплой и высокий uptime.
Локально независимые фронт/бэк не связывай `Blocked by`; проверь интеграцию после обеих задач. К проверке прод должен работать.

## Code Review Rules

- Пиши заголовки и пояснения замечаний к PR на русском языке. Имена файлов, идентификаторы, команды и метки приоритета сохраняй без перевода.

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
- Локально: Docker Compose с Go API, PostgreSQL 18, Redis 8 и SeaweedFS (S3).
- `runtime-agent/`: Python 3.13 сервис systemd; одна VPS, одна сессия, один контейнер/kernel.
  SESSION_ID и KERNEL_LANGUAGE задаются при запуске. SQLite и токен — только на хосте.
  Jupyter — закрытый TCP без host mounts; сеть и firewall реализует infra (роль `runtime_host`) по docs/runtime-agent-network.md.

Нужны Go, C-компилятор (`-race`), Make, curl, Docker с Compose. `.env` — см. [.env.example](.env.example). Команды из корня:

```bash
go mod download       # скачать зависимости
go mod tidy -diff      # проверить согласованность go.mod/go.sum
make build            # собрать bin/server
make test             # тесты с -race, coverage.out и итоговое покрытие
make lint             # статический анализ
make docker-build     # собрать colab-backend:local
make doctor           # инструменты, настройки и GitHub-доступы; ничего не меняет
make check-fast       # gofmt и go vet; затем тесты изменённого пакета
make check            # tidy, сборка всех пакетов, race-тесты, lint перед PR
make up               # собрать и запустить API, PostgreSQL, SeaweedFS; ждать health
make health           # GET /health; make logs — последние логи; make down — остановка
make run              # то же окружение с логами в терминале
make agent-setup agent-lint agent-test agent-build # Python-агент: установка и проверки
make agent-images agent-integration # Docker-образы Python/R и интеграционные тесты
make agent-network-check # отдельная VPS с firewall infra; не обычный CI. smoke: — docs/runtime-provisioning.md
```

`make test` и CI используют `scripts/test.sh`: тестируются все пакеты с `-race -count=1 -coverpkg=./...` (включая вызовы из тестов других пакетов). Из `coverage.out` исключаются `internal/transport/http/api`, `cmd/apidog`, `migrations` и `cmd/main`. Логика приложения в `internal/app` учитывается. Последняя строка — итоговое покрытие; CI пишет ту же строку в summary. Цель — ≥50%, автоматического порога нет. Отчёт: `go tool cover -func=coverage.out`.

Запускай клиент агента из корня backend с `go.mod`, включая отдельный worktree.
Настройка Codex/Claude Code, gopls и сценарий новой сессии — в
[docs/local-development.md](docs/local-development.md), раздел «Harness AI-агента».
Сначала вызови `go_workspace`, затем `go_search`;
после чтения Go-файла — `go_file_context`, перед изменением символа —
`go_symbol_references`, после изменений — `go_diagnostics` и тесты пакета.
Если MCP недоступен, явно укажи это; `rg`, `go vet` и тесты дают запасной путь.
Для Python: Serena `initial_instructions` → `activate_project` для `runtime-agent/`;
через `serena_repl` изучи `s.info("lsp")`, затем `s.lsp.get_symbols_overview` / `find_symbol`.
Перед изменением символа — `s.lsp.find_referencing_symbols`; после — диагностика файла,
Pyright с Python из `runtime-agent/.venv` и тесты агента. Без MCP — явно укажи это и используй `rg`, Pyright и тесты.
Перед PR нужны `make check` и `make docker-build`; результаты и ограничения — в PR.

## Паттерны кода

В `cmd/` размещай только точки входа в приложение; допустим README с описанием микросервиса. Тесты в `cmd/` запрещены: проверяемую логику и её тесты размещай в `internal/` (сборка и запуск API — `internal/app`).

Карта кода: `cmd/main` → `internal/app` (сборка и запуск), `internal/config` — окружение.
`internal/runtime` — VPS сессий (воркер, OpenStack, Ansible), `cmd/runtime-smoke` — ручная проверка;
`internal/transport/http` — роутер, `/health`, API-генерация, middleware, security, principal, httperr; `internal/{user,notebook}/delivery/http`
→ `usecase` (сценарии, порты) → `domain`; `adapters/{postgres,s3,security,ipynb}`
реализуют порты. Usecase не импортирует HTTP или адаптеры; domain не знает о них.
Сессии — Redis primary через `usecase.SessionStore`, пользователи — PostgreSQL; [runbook](docs/redis-sessions.md).
`internal/tooling/apidog` — экспорт контракта; `migrations` — встроенные goose SQL.
`internal/transport/http/api/api.gen.go` не редактируй вручную. Контракт и `make generate` описаны в [docs/apidog.md](docs/apidog.md).

Сохраняй контекст запроса при передаче дальше; для операций БД из обработчика используй `r.Context()`. Порядок: request ID → logging → recover → S2S → Bearer → ограничение тела блокнотов (512 КиБ) → Validator. Общие middleware оборачивают весь роутер, чтобы request ID был на всех ответах, включая S2S-отказы, 404 и 405. API обслуживает только BFF; `BFF_API_KEY` обязателен, `GET /health` исключён.

## Границы backend и эксплуатация

Общепроектный контекст, действующая архитектура и планы развития проекта — в [Контекст проекта](https://docs.cellestial.ru/project-context).
Спеки и планы реализации конкретной задачи размещай в репозитории, где выполняется
работа; общие для нескольких компонентов решения отражай в `docs`.
Факты о реализации проверяй по актуальному коду и конфигурации.

- Backend отвечает за Go API, данные и бизнес-логику; местный Compose — для разработки.
- Агент сам VPS не создаёт: создание, настройку (воркер запускает playbook) и удаление runtime-VPS выполняет
  `internal/runtime` ([docs](docs/runtime-provisioning.md)). Роли (`runtime_host`, `runtime_agent`, `site.yml`, mount `/app/deploy/runtime`): установка агента/systemd, mTLS-доступ Go API
  к агенту, сеть/firewall и секреты — в `infra`; запускает их воркер `internal/runtime`.
- `infra`: Pulumi, Ansible Vault, production Compose и Caddyfile. Не переноси
  production-конфигурацию в backend и не коммить открытые секреты.
- Проект Compose на VPS — `cellestial`, каталог `/opt/cellestial`; существующие
  volumes Caddy сохраняются. Миграции goose выполняются при старте API.
- Файлы блокнотов хранятся в S3: нужен `S3_NOTEBOOKS_BUCKET` и параметры `AWS_*`.
  Локально Compose использует SeaweedFS, production — Selectel.
- `ci.yml` проверяет код и публикует образ, затем вызывает `cd.yml` через
  `workflow_call` с SHA-тегом из job `docker`. CD в environment `production`
  запускает `deploy-backend.yml` из `infra/main`. Inventory статический;
  Selectel credentials и Pulumi в CI не используются.
- Merge в `main` требует review ментора, кроме YarikMix; отдельного approval environment нет.
  Деплой только из `main`, без отмены выполняющейся выкатки.
- Откат — тот же playbook с прежним SHA-тегом, без автоматических миграций Down.

Фактический [CI backend](.github/workflows/ci.yml) публикует
`ghcr.io/cringe-driven-development-team/backend`: теги `sha-<short>` и `main`,
без `latest`. Пакет приватный; job выкатки передаёт временный `GITHUB_TOKEN`
с `packages: read` через окружение Ansible. Постоянного GHCR-токена в Vault нет.
Выкатывается SHA-тег; строго неизменяемая ссылка — digest.
Deploy требует PRODUCTION_ENABLED=true; sync — COURSE_SYNC_ENABLED=true,
needs lint/test/docker, независимо от deploy. Повтор sync принимает только ci_run_id.
Pull старого GHCR path требует old sha-tag и прежний read credential.
Без registry используй проверенный сохранённый образ и manual override из
infra/ansible/README.md: pull_policy never, только API, без миграций Down.
## Коммиты

Формат: `<тип>: <описание>` или `<тип>(<область>): <описание>`.
Типы: `feat`, `fix`, `refactor`, `style`, `test`, `docs`, `chore`.

```text
docs: добавить инструкции для агента
refactor(auth): вынести проверку токена в middleware
```

Назначение типов — в [README](README.md#типы-коммитов).

## Документация

README содержит краткую, актуальную информацию о проекте и его использовании. Порядок выкатки конкретной задачи, временные зависимости и условия слияния описывай в задаче при необходимости, а не в README. Подробную общепроектную документацию размещай в репозитории организации [docs](https://github.com/Cringe-Driven-Development-Team/docs). Локальные инструкции backend по запуску, API и CI размещай в `docs/`, оставляя README коротким навигатором.

## Актуальность документа

При изменении команд или архитектурных границ обновляй этот файл в том же изменении, размер этого документа не более 150 строк.
