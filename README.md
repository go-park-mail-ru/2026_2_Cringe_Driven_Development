# Backend Colab

Go API учебного проекта «Colab». Общепроектный контекст, текущую архитектуру и планы ищите на [сайте документации](https://docs.cellestial.ru/project-context). Этот README — точка входа в backend: запуск, код и рабочие правила.

## Быстрый старт

Нужны Go версии из [go.mod](go.mod), C-компилятор для тестов с `-race`, Make и Docker с Compose. Локальный Compose запускает API, PostgreSQL, Redis и SeaweedFS (S3). Секреты задаются только в `.env`, файл не коммитьте.

```bash
cp -n .env.example .env
# Задайте в .env разные JWT_SECRET и BFF_API_KEY.
make doctor
make up
make health  # ожидается {"status": "alive"}
make down    # volumes сохраняются
```

`make up` ждёт healthcheck до 120 секунд. При отказе используйте `make logs` и `docker compose ps -a`. `/health` проверяет ответ API, но не все операции с БД и S3. Полный список команд, настройки портов и окружения — в [локальной разработке](docs/local-development.md).

## Агент исполнения Python/R

[runtime-agent](runtime-agent/README.md) — Python 3.13 сервис systemd на выделенной
VPS пользовательской сессии. Запускает один контейнер с одним Python или R kernel,
принимает команды через внутренний HTTP/WebSocket API и хранит журнал SQLite.
UUID сессии и язык задаются при запуске; лимиты — 1 CPU / 1 GiB RAM, простой — 15 минут.
Контейнер без host mounts; связь агента с Jupyter — закрытый TCP без публикации порта.
Доступ kernel к интернету ограничивает firewall infra по [контракту сети](docs/runtime-agent-network.md).

Создание, настройку и удаление VPS сессий в Selectel выполняет модуль `internal/runtime`:
[docs/runtime-provisioning.md](docs/runtime-provisioning.md).

Проверки: `make agent-setup agent-lint agent-test agent-build`;
с Docker — `make agent-images agent-integration`; локальный Python kernel —
`make agent-test-jupyter`. Сетевая приёмка на отдельной VPS — `make agent-network-check`.
VPS создаёт и удаляет backend; роли установки агента, реализация firewall и секреты
принадлежат `infra`. Интеграция с пользователями и ipynb выполняется отдельно.

## Wiki кода

Собственная `openwiki/quickstart.md` после ручной генерации описывает код Go API на исходном SHA.
Проверка: `bun scripts/openwiki-check.ts --against <полный актуальный SHA backend/main>` (нужен Bun).
До генерации используйте документацию и исходники ниже; stale wiki служит только указателем.
[Brief генерации](openwiki/INSTRUCTIONS.md) задаёт корпус и границы; общий поиск четырёх репозиториев
настраивается по [README docs](https://github.com/Cringe-Driven-Development-Team/docs#readme).
Без MCP читайте Markdown нужного клона. Код и wiki не доказывают состояние прода.

Изменения `openwiki/**` в `main` запускают [Publish docs wiki](.github/workflows/publish-docs-wiki.yml):
он вызывает `pages.yml` в `docs/main`; для диагностики доступен ручной запуск.
Нужен secret `DOCS_PAGES_TOKEN` с Actions: write только для репозитория docs;
настройка и публичная wiki — в [README docs](https://github.com/Cringe-Driven-Development-Team/docs#readme).
Локальная проверка dispatch без GitHub: `bun test scripts/openwiki-publish.test.ts`.

## Код и документация

- `cmd/main` — точка входа; `internal/app` собирает зависимости, `internal/config` читает окружение.
- `internal/transport/http` — маршруты, авторизация и HTTP-политики; `internal/{user,notebook}` — domain, usecase, HTTP-обработчики и адаптеры хранения.
- `migrations/` — SQL-миграции goose; `internal/transport/http/api/api.gen.go` — код, сгенерированный из Apidog, вручную не редактируйте.
- [Сессии Redis](docs/redis-sessions.md) — конфигурация, отзыв, переключение и восстановление.
- [Авторизация через BFF](docs/authentication.md) — JSON-токены, Bearer и обязательный S2S-ключ.
- [Редактирование ipynb](docs/notebook-editing.md) — операции над блоками, хеши и консистентность.
- [Контракт API в Apidog](docs/apidog.md) — ветки, Merge Request и `make generate`.
- [CI, образ и деплой](docs/release.md) — проверки и выпуск. Production-конфигурация принадлежит [infra](https://github.com/Cringe-Driven-Development-Team/infra).
- [AGENTS.md](AGENTS.md) — локальные правила для агента; Claude Code подключает их через [CLAUDE.md](CLAUDE.md).

Состав команды — в [профиле организации](https://github.com/Cringe-Driven-Development-Team/.github/tree/main/profile).

## Как работать с задачами

Задачи команды собраны на [доске Scrumban](https://github.com/orgs/Cringe-Driven-Development-Team/projects/1).
Правила оформления задач и ревью — в [CONTRIBUTING команды](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md).
Перед передачей изменений выполните `make check` и `make docker-build`.

## Типы коммитов

Формат: `<тип>: <описание>` или `<тип>(<область>): <описание>`. Коммиты ветки сохраняются в истории как есть.

| Тип | Когда использовать |
| --- | --- |
| `feat` | новая функциональность |
| `fix` | исправление бага |
| `refactor` | изменение кода без изменения поведения |
| `style` | форматирование |
| `test` | тесты |
| `docs` | документация |
| `chore` | конфиги, зависимости, сборка и CI |
