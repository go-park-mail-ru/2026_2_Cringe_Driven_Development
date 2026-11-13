# Контракт API: OpenAPI в Git

Единственный редактируемый исходник HTTP-контракта — [`spec/openapi.json`](../spec/openapi.json)
(OpenAPI 3.1, JSON). Контракт меняется обычным PR в backend вместе с generated-кодом;
Apidog в изменении, генерации и проверке контракта не участвует.
Frontend получает копию `spec/openapi.json`, [`spec/redocly.yaml`](../spec/redocly.yaml),
[`spec/plugins/`](../spec/plugins) и закрепление Redocly
([`spec/package.json`](../spec/package.json), `spec/package-lock.json`) по полному Git SHA
этого репозитория и свою копию не редактирует.

## Команды

Нужен Node.js ≥ 22.12 с npm. Сеть требуется только для первой установки Redocly.

```bash
make contract-tools   # npm ci в spec/: Redocly CLI закреплённой версии
make contract-lint    # проверка структуры, ссылок, примеров и default
make generate         # contract-lint, затем oapi-codegen → internal/transport/http/api/api.gen.go
make contract-check   # generate и ошибка, если generated-код не совпадает с закоммиченным
```

`make check` и CI (job `contract`) запускают `make contract-check` на каждом PR и push в `main`.
Сгенерированный `api.gen.go` руками не правится; контракт и код обновляются в одном PR.

## Проверка Redocly

`spec/redocly.yaml` расширяет `recommended-strict`: любая находка — ошибка с ненулевым кодом.
Отключены только `info-license` и `no-empty-servers` (базовый URL `/api/v1` задают BFF и Go);
неиспользуемый ответ `Conflict` из исходного снимка — предупреждение.
Примеры запросов, ответов и параметров сверяются со схемами без лишних полей.
Redocly сам не проверяет `default`, поэтому плагин `spec/plugins/defaults.js` перед
проверкой добавляет `default` в `examples` схемы, и его проверяет `no-invalid-schema-examples`.
Ignore-файл Redocly не используется.

## Правила изменения контракта

- Общие схемы — в `components`, ссылки через `$ref`. Рядом с `$ref` допустимы только
  `summary` и `description`; реальные заголовки ответа описываются в Response Object.
- Политика дополнительных полей явная: `additionalProperties: false` для закрытых
  объектов, `true` — только для открытых payload (события, nbformat outputs).
- Используй только подмножество OpenAPI, которое поддерживает генератор frontend
  ([frontend-packages#63](https://github.com/Cringe-Driven-Development-Team/frontend-packages/issues/63)).
  Новое неподдерживаемое ограничение сначала согласуй с генератором; молча ослаблять
  схемы, чтобы обойти проверку или генерацию, нельзя.
- Каждый пример и `default` соответствуют схеме: required, format, pattern и границы.
- Breaking changes не запрещены автоматически; для ревью можно запустить
  [oasdiff](https://github.com/oasdiff/oasdiff) по старой и новой версии файла.

Для агентов подключены официальные skills Redocly: `.agents/skills/redocly-cli` и
`.agents/skills/redocly-lint-rules` (Claude Code видит их через `.claude/skills`).

## Генерация Go

`internal/transport/http/api/cfg.yaml` генерирует strict server, модели и встроенную
спецификацию только для операций из `output-options.include-operation-ids` — всех тридцати девяти
операций: авторизация и TOTP 2FA ([описание](two-factor-authentication.md)), профиль,
двенадцать операций блокнотов, включая переименование, и пятнадцать операций runtime-сессий
([описание](runtime-sessions.md)). Новую операцию добавляй в этот список вместе с реализацией.

`middleware.Validator` проверяет запросы по встроенной спецификации; загрузка аватара
валидируется без буферизации multipart-тела.

## Контракт BFF

Контракт описывает единственный серверный API для BFF: токены в JSON, Bearer для данных
и logout. Пути указаны относительно `/api/v1`. Обязательный заголовок `X-BFF-Key` описан у
всех операций; проверка в middleware предшествует Bearer и валидации. Отказ — `403 s2s_forbidden`.
Cookie-auth и браузерных параметров в контракте нет.

## Поиск и страницы блокнотов

`GET /api/v1/notebooks` возвращает объект
`{ items, next_cursor, matched_count, total_count }`. `items` содержит прежние
`NotebookSummary`, включая `preview_code` и `has_active_session`.

```bash
curl --get "$API_URL/api/v1/notebooks" \
  -H "X-BFF-Key: $BFF_API_KEY" -H "Authorization: Bearer $ACCESS_TOKEN" \
  --data-urlencode 'search=note' --data-urlencode 'limit=24'
# Продолжение: передайте next_cursor из предыдущего ответа без изменений.
curl --get "$API_URL/api/v1/notebooks" \
  -H "X-BFF-Key: $BFF_API_KEY" -H "Authorization: Bearer $ACCESS_TOKEN" \
  --data-urlencode 'search=note' --data-urlencode 'limit=24' \
  --data-urlencode "cursor=$NEXT_CURSOR"
```

`search` ищет буквальную подстроку имени без учёта регистра, обрезая пробелы по
краям; `%`, `_` и `\` не являются шаблонами. Без поиска или с пробельным поиском
выбирается весь список владельца. `limit` по умолчанию 24, допустимы 1–100.
`matched_count` считает все совпадения владельца, `total_count` — все его блокноты;
оба значения не зависят от текущей страницы. При отсутствии совпадений `items: []`.
`next_cursor: null` означает конец списка, в том числе для полной последней страницы.

Порядок — `updated_at DESC, id DESC`. Курсор привязан к владельцу и точной строке
после обрезки пробелов, включая регистр; `limit` между страницами можно менять.
Явно пустые `limit`/`cursor`, неверный размер страницы, повреждённый или выданный
для другого владельца/поиска курсор дают `400 validation_error`.

Внутри ответа счётчики и карточки читаются из одного снимка PostgreSQL. Между
запросами снимок не удерживается: изменение метаданных может переместить блокнот
через границу курсора. Обход без пропусков и дублей гарантирован для неизменного
списка. Для актуального начала списка нужен новый запрос без курсора. Возврат
во вкладку сам по себе не требует перестройки уже загруженных карточек — это
согласованное поведение клиента в frontend#95.
