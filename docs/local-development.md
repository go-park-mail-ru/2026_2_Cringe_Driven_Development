# Локальная разработка backend

## Запуск и проверки

Нужны Go из `go.mod`, C-компилятор для `-race`, Make, Docker Compose и Node.js
с npm для проверки контракта. Для агента нужен Python 3.13.

1. Скопируйте `.env.example` в `.env`. Заполните JWT и BFF secrets разными
   случайными значениями. Для TOTP и runtime используйте разные ключи согласно
   примеру. Go вне Compose запускайте с `RUNTIME_PROVIDER=fake`.
2. Выполните `make doctor` и `make up`. Compose поднимает Go API, PostgreSQL,
   Redis и S3-совместимый SeaweedFS. `make health` проверяет готовность,
   `make logs` показывает логи, `make down` останавливает сервисы.
3. Для отдельной сборки используйте `make build`. Перед проверкой полного diff
   запустите `make check-fast` и тесты изменённых пакетов.

## Логи

- Go пишет access log и добавляет `request_id` из контекста. Вызовы Go к агенту
  передают `X-Request-ID`. Агент проверяет заголовок или генерирует собственный ID.
- `LOG_LEVEL=debug` включает сводки `storage.operation` в Go. PostgreSQL,
  Redis и S3 инструментированы на границе драйвера. Агент пишет access log и SQLite-сводки в JSON. Для SQLite задайте
  `LOG_LEVEL=debug` в окружении агента. Поля содержат тип хранилища, операцию, длительность и исход.
- Логи не включают SQL, аргументы, токены, ключи объектов и тела запросов.
  Для фоновых операций используйте существующие идентификаторы сессии или lease,
  если HTTP-контекст уже завершён.

## Harness AI-агента

Запускайте клиент в корне checkout с `go.mod`. Используйте gopls MCP для Go
и Serena для `runtime-agent/`. Проверяйте callers перед изменением портов,
а после правок запускайте диагностику и профильные тесты. Для Pyright
используйте интерпретатор из `runtime-agent/.venv`.

## Генерация тестовых моков

`bash scripts/generate-mocks.sh` вызывает закреплённую в `go.mod` версию mockgen.
После изменения портов повторите генерацию и проверьте воспроизводимость diff.

## Redis для авторизации

[Сессии, ключи и восстановление Redis](redis-sessions.md).

## S3 и аватары

Локально `s3-init` настраивает бакет и публичное чтение аватаров. Go использует
`S3_AVATARS_BUCKET`, `S3_AVATARS_PUBLIC_URL` и настройки `AWS_*`.
Production S3/CDN настраивает infra. [Загрузка аватара](user-profile.md#загрузка-аватара).

## Агент исполнения Python

`make agent-setup agent-lint agent-test agent-build` проверяет код и собирает пакет.
`make agent-images agent-integration` требует Docker и проверяет запуск kernel.
`make agent-test-jupyter` проверяет транспорт Jupyter отдельно.
[Сессии и исполнение](runtime-sessions.md) · [Подготовка VPS](runtime-provisioning.md).

## Проверки перед PR

- `make check` проверяет release-скрипты, форматирование, vet, зависимости,
  сборку, race-тесты, линтер и OpenAPI. `make docker-build` проверяет образ API.
- `make test` формирует `coverage.out`. Порог 60% сравнивается до округления.
  Сгенерированные Go-файлы, `cmd/main` и `migrations` исключены из метрики.
  Отчёт можно открыть через `go tool cover -func=coverage.out`.
- CI предоставляет PostgreSQL и Redis. Для локальных интеграционных тестов
  задайте `TEST_POSTGRES_DSN`, `RUNTIME_TEST_DATABASE_URL`, `REDIS_TEST_ADDR`
  на выделенных тестовых сервисах. Сценарий восстановления Redis также требует
  `REDIS_RECOVERY_TESTS=1` и доступный `redis-cli`.
- Для изменений агента выполните его проверки и приложите их результаты.

## Как работать с задачами

Рабочая ветка — `api-N` в репозитории backend. Правила ревью, задачи и доска
описаны в [CONTRIBUTING команды](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md).
Правила именования и открытия PR указаны в [AGENTS.md](../AGENTS.md#git-и-задачи).

## Типы коммитов

`feat` добавляет функциональность, `fix` исправляет поведение, `refactor`
упрощает реализацию, `style` меняет форматирование, `test` добавляет проверки,
`docs` меняет документацию, `chore` меняет сборку, конфигурацию или зависимости.

## Код и документация

Сверяйте руководство с кодом и контрактом в том же изменении. Общие границы
компонентов ведите в командном docs. Локальные команды и runbooks ведите здесь.

## Файлы runtime

Для файлового интерфейса нужен дисковый WORKSPACE_PATH из infra #161. Подготовка
каталогов, контракт и дополнительные проверки описаны в
[руководстве workspace](workspace-files.md).
