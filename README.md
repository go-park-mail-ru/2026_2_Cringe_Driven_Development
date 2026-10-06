# 2026_2_Cringe_Driven_Development

[![CI](https://github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/actions/workflows/ci.yml)

Backend-репозиторий проекта «Colab» команды «Cringe Driven Development»

## CI и Docker-образ

На каждом PR запускаются `lint` (golangci-lint), `test` (проверка зависимостей
через `go mod tidy -diff`, сборка и тесты с race detector) и `docker` (сборка образа
без публикации). Покрытие выводится в summary запуска; минимального порога нет.
Новый push в PR отменяет предыдущие проверки этого PR. Запуски `main` не отменяют
друг друга; выкатки сериализуются отдельно и требуют ручного approval.

После push в `main`, если все проверки прошли, образ публикуется в
`ghcr.io/go-park-mail-ru/2026_2_cringe_driven_development`:

- `sha-<short>` — тег конкретного коммита для выбора версии при деплое;
- `main` — последняя успешно опубликованная сборка основной ветки.

Тег `latest` не публикуется. SHA-тег может быть перезаписан повторным запуском
для того же коммита; для строго неизменяемой ссылки используется digest образа.
После публикации `ci.yml` вызывает reusable workflow `cd.yml`, передавая SHA-тег
из job `docker`. CD запускает Ansible из ветки `main` репозитория `infra`
и выкатывает этот образ на единственную VPS Selectel.

Для локальной работы нужны Go версии из `go.mod`, компилятор C для `-race`,
Make и Docker с Compose. Команды:

```bash
make lint          # статический анализ
make test          # тесты с race detector и coverage.out
make build         # бинарник bin/server
make docker-build  # образ colab-backend:local
make run           # API и PostgreSQL через Docker Compose
```

При необходимости скопируйте `.env.example` в `.env` и измените локальные
настройки. После `make run` API доступен по `http://localhost:8080/health`
(если порт не изменён). Остановка: `docker compose down`.

CORS для запросов с `credentials: "include"` настраивает Go API через
`CORS_ALLOWED_ORIGINS`. Если переменная отсутствует, разрешены
`https://cellestial.ru` и `http://localhost:5173`; явно пустое значение выключает
CORS. Заданный список заменяет значения по умолчанию: укажите все нужные origin
через запятые или пробелы, со схемой и без завершающего слэша. Другие порты,
`127.0.0.1` и dev-стенды добавляются явно; `*` не допускается.

Проверка preflight без авторизации (ожидается `204` с точным разрешённым origin,
`Access-Control-Allow-Credentials: true`, методами и заголовками):

```bash
curl -i -X OPTIONS http://localhost:8080/api/v1/users/me \
  -H 'Origin: http://localhost:5173' \
  -H 'Access-Control-Request-Method: GET' \
  -H 'Access-Control-Request-Headers: Authorization, Content-Type, X-Request-ID'
```

В браузере фронт должен использовать `credentials: "include"`; разрешающие
CORS-заголовки приходят и на ошибках API. Политика cookie (`SameSite`/`Secure`)
настраивается отдельно и этим изменением не расширяется.

Пакет GHCR остаётся приватным: организация `go-park-mail-ru` запрещает публичные
пакеты. CI публикует его с `GITHUB_TOKEN`; job выкатки получает `packages: read`
и передаёт собственный временный `GITHUB_TOKEN` в Ansible через окружение.
Постоянный PAT для production и отдельный GHCR secret не нужны. Для `main`
администратор включает обязательные проверки `lint`, `test`, `docker`.

Если публикация завершается с 403, передайте ментору ссылку на запуск для
проверки прав организации и пакета. Личные токены для обхода ограничений
не используются; CI работает с `GITHUB_TOKEN`.

## Авторизация

API принимает access-токен только из cookie `access_token`; заголовок
`Authorization` для авторизации не используется и в ответах не возвращается.
Login, register и refresh выдают две отдельные cookie:

| Cookie | Path | Срок жизни |
| --- | --- | --- |
| `access_token` | `/api/v1` | `ACCESS_TOKEN_TTL` (по умолчанию 15 минут) |
| `refresh_token` | `/api/v1/auth` | Оставшийся срок refresh-сессии (`REFRESH_TOKEN_TTL`, по умолчанию 30 дней) |

Обе cookie имеют `HttpOnly` и `SameSite=Lax`, без `Domain`. Настройка
`COOKIE_SECURE=true` включает `Secure` для обеих cookie; в production с HTTPS
она обязательна. Для локального HTTP используется `COOKIE_SECURE=false`.
Браузерный клиент отправляет запросы с `credentials: "include"` и не читает JWT.
При истечении access-токена закрытая ручка отвечает 401; клиент вызывает
`POST /api/v1/auth/refresh`, получает новые cookie и повторяет запрос.
Успешный logout удаляет обе cookie с исходными путями и отзывает refresh-сессию.

Пример для локального API: curl сохраняет cookie в файл и отправляет их сам.
Используйте тестовую учётную запись; файл cookie содержит токены, не коммитьте его.

```bash
cookie_jar=$(mktemp)
curl --fail-with-body -i -c "$cookie_jar" \
  -H 'Content-Type: application/json' \
  -d '{"login":"bob","password":"password123"}' \
  http://localhost:8080/api/v1/auth/login
curl --fail-with-body -b "$cookie_jar" http://localhost:8080/api/v1/users/me
curl --fail-with-body -i -b "$cookie_jar" -c "$cookie_jar" -X POST \
  http://localhost:8080/api/v1/auth/refresh
curl --fail-with-body -i -b "$cookie_jar" -c "$cookie_jar" -X POST \
  http://localhost:8080/api/v1/auth/logout
curl -i -b "$cookie_jar" http://localhost:8080/api/v1/users/me # ожидается 401
rm -f "$cookie_jar"
```

Переход на cookie выкатывается только вместе с готовым frontend и CSRF-защитой
backend#4. После её подключения изменяющие запросы, включая login и refresh,
требуют `X-CSRF-Token`: перед login получите анонимную CSRF-cookie запросом к API,
затем отправляйте текущее значение CSRF-cookie в заголовке каждого изменяющего
запроса. Пример выше показывает обмен auth-cookie до подключения #4.

## Production-деплой

На VPS один проект Compose `cellestial` в `/opt/cellestial`: Caddy, Go API и
Postgres. Caddy проксирует `/api/v1/*` в API с сохранением пути; API и БД не публикуют
порты наружу. Production Compose, Caddyfile, Vault и playbook принадлежат `infra`;
местный `docker-compose.yml` остаётся окружением разработки. Пути клиента
вне `/api/v1/*` Caddy продолжает обслуживать из S3.

После успешных `lint`, `test`, `docker` на push в `main` CI вызывает
[CD](.github/workflows/cd.yml) через `workflow_call`. Его job `deploy` ждёт approval
Environment `production`. В настройках GitHub нужно разрешить только ветку `main`
и назначить required reviewers `blackHATred` и `YarikMix`: достаточно одного
одобрения. Это внешняя настройка GitHub, workflow сам reviewers не создаёт.

Первоначальная подготовка:

- Пакет остаётся приватным и доступен `GITHUB_TOKEN` курсового репозитория.
  Ansible временно авторизует VPS для pull и делает logout даже при ошибке.
- Денис создаёт отдельный CI-ключ, коммитит `ci.pub` в infra и устанавливает ключ
  через `site.yml`. Проверенный host key берёт с доверенного подключения.
- Ярослав или Александр добавляет environment secrets `DEPLOY_SSH_KEY`,
  `SSH_KNOWN_HOSTS`, `ANSIBLE_VAULT_PASSWORD`. Личные SSH-ключи для CI не используются.
- Существующие `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TELEGRAM_TOPIC_ID` доступны
  job без дублирования. Результат и ссылка на запуск отправляются в основной топик.
- Сначала влить infra#22 и PR, закрывающий infra#30, затем установить `ci.pub`
  через `site.yml` и настроить environment. Backend PR мержится последним;
  job выкатки получает `deploy-backend.yml` из `infra/main`.

Деплой пишет пароль Vault и SSH-файлы во временный каталог runner, проверяет host key,
запускает `deploy-backend.yml` со статическим inventory и удаляет временные файлы.
В CI не нужны Selectel credentials, доступ к Pulumi state и `pulumi up`.

Только шаг с playbook получает `GHCR_USERNAME` из `github.actor` и `GHCR_TOKEN`
из `secrets.GITHUB_TOKEN`. Токен не передаётся через `-e`, не записывается в Vault
или `.env` API. Ansible выполняет login через stdin с `no_log`, скачивает API,
всегда делает logout и запускает уже скачанный образ. Отсутствие credentials
останавливает playbook до изменений VPS. Анонимный HTTP 401 для пакета ожидаем.

Playbook ждёт healthcheck `/health` внутри Compose и проверяет через HTTPS
`/api/v1/users/me`: без токена ожидается JSON с HTTP 401. Миграции встроены в образ и
выполняются при старте; отдельного шага миграций нет. Ошибка Telegram показывается
предупреждением и не подменяет результат деплоя.

Параметры S3 из `.env.example` роль `app` передаёт из infra vars и Vault.
Go API хранит файлы блокнотов в бакете `S3_NOTEBOOKS_BUCKET`, используя
`AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` и `AWS_ENDPOINT_URL_S3`.
Локальный Compose использует SeaweedFS, production — Selectel. Параметры
аватарок передаются конфигурацией, но текущий API их ещё не читает.

Откат выполняется из infra тем же playbook с прежним опубликованным SHA-тегом,
см. [инструкцию Ansible](https://github.com/Cringe-Driven-Development-Team/infra/blob/main/ansible/README.md).
Миграции Down автоматически не запускаются: прежний бинарник должен быть совместим
с уже применённой схемой. SHA-тег может быть перезаписан повторной сборкой; перед
откатом сверить digest с исходным запуском публикации.

Клиент не перезагружает открытые вкладки автоматически. Go API N+1 должен
обслуживать клиента N: ручки и поля не удаляются в релизе, где клиент перестаёт их
использовать. Совместимость проверяется на ревью контракта в Apidog.

## Ссылки

- [Сервис cellestial.ru](https://cellestial.ru)
- [Доска задач](https://github.com/orgs/Cringe-Driven-Development-Team/projects/1)
- [Репозиторий фронтенда](https://github.com/frontend-park-mail-ru/2026_2_Cringe_Driven_Development)
- [Организация команды](https://github.com/Cringe-Driven-Development-Team)

## Участники команды

1. [Ерофей Гаранин](https://github.com/ManInTheCoat)
2. [Истратов Денис](https://github.com/iRedTea)
3. [Шпакова Дарья](https://github.com/GrayMouse9)
4. [Кунев Валентин](https://github.com/MrDuckVC)

## Менторы

- [Михалёв Ярослав](https://github.com/YarikMix) — _Frontend_
- [Батовкин Александр](https://github.com/blackHATred) — _Backend_
- [Ченцова Дарья](https://t.me/dewon_d) — _UX_

## Как работать с задачами

Код лежит здесь, а задачи — в
[Cringe-Driven-Development-Team/backend](https://github.com/Cringe-Driven-Development-Team/backend)
и на общей [доске](https://github.com/orgs/Cringe-Driven-Development-Team/projects/1) вместе с фронтовыми

1. **Завести задачу** в репозитории `backend`. Как завести и что писать в описании —
   в [гайдлайне организации](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md#как-завести)

2. **Взять задачу.** На доске выбрать карточку из `Ready`, поставить себя
   в `Assignees`, перевести в `In progress`

3. **Создать ветку.** Открыть задачу → в правой колонке `Development` →
   **`Create a branch`**. В `Repository destination` выбрать
   `go-park-mail-ru/2026_2_Cringe_Driven_Development` (в поиске — `2026_2`),
   имя ветки заменить на `api-<номер задачи>`, например `api-12`.
   Затем `Create branch` и локально:

   ```bash
   git fetch
   git switch api-12
   ```

4. **Закоммитить** по шаблону `<тип>: <описание>`, типы — в таблице ниже.
   Область в скобках после типа указывать необязательно:

   ```
   feat: добавить эндпоинт регистрации
   fix: не отдавать 500 при пустом теле запроса
   refactor(auth): вынести проверку токена в middleware
   ```

5. **Открыть pull request** в `main`, когда код готов к ревью.
   Заголовок — по шаблону `API-<номер задачи>: <название задачи>`, например
   `API-12: Эндпоинт регистрации`. В описании — **обязательно** строка

   ```
   Closes Cringe-Driven-Development-Team/backend#12
   ```

   Почему ссылка полная и что писать, если задача затрагивает ещё один репозиторий, —
   в [гайдлайне](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/CONTRIBUTING.md#как-закрыть).
   Убедиться, что в правой колонке PR в блоке `Development` указана задача

6. **Получить апрув** от [Александра](https://github.com/blackHATred)

7. **Влить в `main`** через `Merge pull request`.
   Задача закроется сама, карточка уедет в `Done`

> [!TIP]
> Ветку, созданную руками (`git switch -c api-12 origin/main`), с задачей свяжет
> та же строка `Closes …` — поэтому она обязательна всегда

## Типы коммитов

> [!NOTE]
> Коммиты ветки попадают в `main` как есть, поэтому от их качества зависит
> читаемость истории проекта. Сверху добавляется merge-коммит с названием
> pull request

| Тип | Когда используется |
|---|---|
| `feat` | новая функциональность |
| `fix` | исправление бага |
| `refactor` | код переписан, поведение не изменилось |
| `style` | форматирование и отступы, логика не тронута |
| `test` | тесты |
| `docs` | документация |
| `chore` | конфиги, зависимости, сборка, CI |

## Статусы на доске

| Статус | Что означает |
|---|---|
| `Backlog` | задача заведена, но не запланирована в спринт |
| `Ready` | взята в спринт, можно брать в работу |
| `In progress` | в работе |
| `In review` | открыт pull request, ждёт ревью |
| `Done` | влито в `main` |

Статусы доска двигает сама по событиям в pull request. Руками нужно только взять задачу в работу — остальное происходит автоматически

## Как менять контракт

Контракт API живёт в [Apidog](https://app.apidog.com/project/1382426) (проект `1382426`).
Источник истины — ветка `main` Apidog, опубликованная документация —
[vb78fyael1.apidog.io](https://vb78fyael1.apidog.io/). Файла `openapi.yaml` в репозитории нет:
`make generate` скачивает спецификацию по API и генерирует `internal/api/api.gen.go`.
Сгенерированный код коммитится и руками не правится

### Личный токен

`make generate` ходит в Apidog с личным токеном каждого разработчика:

1. Apidog → аватар → **Account Settings → API Access Token** → **New**
2. Токен показывается один раз — сразу скопировать
3. Положить в локальный `.env` (он в `.gitignore`):

   ```bash
   APIDOG_TOKEN=<токен>
   ```


### Порядок правки

Ветка `main` в Apidog защищена: правки идут через sprint-ветку и Merge Request

1. Переключатель веток рядом с **APIs** → **New Sprint Branch**.
   Имя — как у ветки в git (`api-16`), источник — `main`
2. Ветка создаётся пустой. Чтобы изменить ручку или схему, её нужно забрать из `main`:
   **Pick from main**. Всё, что не забрано, ветка показывает из `main`
3. Внести правки. Ручки редактируются на вкладке **Design**: вкладка **Request** —
   отладочная, её настройки в контракт не попадают
4. **Merge to main** → выбрать ресурсы → **Create Merge Request**
5. Администратор ветки смотрит изменения и одобряет
6. После слияния — `make generate` без `APIDOG_BRANCH_ID` и PR с кодом

Пока Merge Request на ревью, код можно писать по sprint-ветке:

```bash
APIDOG_BRANCH_ID=<id ветки> make generate
```

В `main` репозитория попадает только код, сгенерированный из `main` Apidog: перед мержем PR
перегенерировать без `APIDOG_BRANCH_ID`

### ID sprint-ветки

Переключатель веток рядом с **APIs** → **Manage Sprint Branches**. ID — серое число
рядом с именем ветки (`# 1389936`). Там же видно, кто в `Branch Admins` и защищена ли ветка

### Что видно в Merge Request

Окно слияния показывает список ресурсов ветки с цветными точками: зелёная — новый,
оранжевая — изменённый, серая — без изменений (не сливается). Клик по изменённому
ресурсу показывает его рядом с версией из `main`.

Ручка с изменённым путём или методом считается новой: в Merge Request она появится как
новая, а старая — как удалённая, без сравнения «было — стало». Сравнить можно, открыв
ручку во вкладке **Preview** в `main` и в sprint-ветке

### Конфликты двух sprint-веток

Слияния по частям нет: изменённый ресурс (ручка или схема целиком) при слиянии
**перезаписывает** версию в `main`. Если две sprint-ветки поменяли один ресурс,
побеждает та, что влита позже, а правки первой молча теряются.

Поэтому перед одобрением Merge Request ревьюер сравнивает изменённые ресурсы с **текущим**
`main`: если там видны откаты чужих правок, ветку нужно обновить. История изменений ресурса
в `main` (**Change History**) позволяет посмотреть слияния и откатить неудачное

### Опубликованная документация

[vb78fyael1.apidog.io](https://vb78fyael1.apidog.io/) показывает только `main`
(**Publish Docs** → сайт → **Source Branch**) и обновляется сам сразу после слияния,
перепубликовывать не нужно. Правки из sprint-веток на сайте не видны, пока их не вольют в `main`.


### Подводные камни

- **Авторизация задаётся на самой ручке.** Auth, выставленный на папке, в Apidog работает,
  но в экспорт (а значит, в `security` и в сгенерированный код) не попадает.
  Для закрытой ручки: **Design → Auth → Security Scheme → схема cookie-авторизации**
  (`type: apiKey`, `in: cookie`, `name: access_token`)
- **Удаления через sprint-ветку не работают.** Удалить ресурс в ветке — значит просто
  перестать его переопределять: он продолжает приходить из `main` и в экспорт, и в
  `make generate`, а при слиянии не удаляется. Ненужные ручки и схемы удаляются
  в `main` отдельно, после слияния.
