# 2026_2_Cringe_Driven_Development

[![CI](https://github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/actions/workflows/ci.yml)

Backend-репозиторий проекта «Colab» команды «Cringe Driven Development»

## CI и Docker-образ

На каждом PR запускаются `lint` (golangci-lint), `test` (проверка зависимостей
через `go mod tidy -diff`, сборка и тесты с race detector) и `docker` (сборка образа
без публикации). Покрытие выводится в summary запуска; минимального порога нет.
Новый push отменяет предыдущий запуск CI для той же ветки или PR.

После push в `main`, если все проверки прошли, образ публикуется в
`ghcr.io/go-park-mail-ru/2026_2_cringe_driven_development`:

- `sha-<short>` — тег конкретного коммита для выбора версии при деплое;
- `main` — последняя успешно опубликованная сборка основной ветки.

Тег `latest` не публикуется. SHA-тег может быть перезаписан повторным запуском
для того же коммита; для строго неизменяемой ссылки используется digest образа.
Деплой выполняется отдельно в `infra`.

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

После первой публикации администратор должен сделать пакет публичным в GHCR,
проверить его связь с репозиторием и включить обязательные проверки
`lint`, `test`, `docker` для `main`. После этого образ можно скачать без входа:

```bash
docker pull ghcr.io/go-park-mail-ru/2026_2_cringe_driven_development:main
```

Если публикация завершается с 403, передайте ментору ссылку на запуск для
проверки прав организации и пакета. Личные токены для обхода ограничений
не используются; CI работает с `GITHUB_TOKEN`.

## Ссылки

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

Все задачи команды, бэковые и фронтовые, живут на одной
[доске](https://github.com/orgs/Cringe-Driven-Development-Team/projects/1)

> [!IMPORTANT]
> Задача заводится issue в той репе, где будет код: бэковые — здесь, фронтовые —
> в [репозитории фронтенда](https://github.com/frontend-park-mail-ru/2026_2_Cringe_Driven_Development).
> Issue, заведённая не в той репе, не свяжется с pull request

1. **Взять задачу.** На доске выбрать карточку из `Ready`, поставить себя
   в `Assignees`, перевести в `In progress`

2. **Создать ветку.** Открыть issue → в правой колонке кнопка
   **`Create a branch for this issue`**. GitHub предложит имя, собранное из заголовка
   задачи, — заменить его на `api-<номер issue>`, например `api-12`.
   Затем `Create branch` и локально:

   ```bash
   git fetch
   git switch api-12
   ```

3. **Закоммитить** по шаблону `<тип>: <описание>`, типы — в таблице ниже.
   Область в скобках после типа указывать необязательно:

   ```
   feat: добавить эндпоинт регистрации
   fix: не отдавать 500 при пустом теле запроса
   refactor(auth): вынести проверку токена в middleware
   ```

4. **Открыть pull request** в `main`, когда код готов к ревью.
   Заголовок — по шаблону `API-<номер issue>: <название issue>`, например
   `API-12: Эндпоинт регистрации`.
   Убедиться, что в правой колонке PR в блоке `Development` указана задача —
   если её там нет, связь потерялась

5. **Получить апрув** от [Александра](https://github.com/blackHATred)

6. **Влить в `main`** через `Merge pull request`.
   Issue закроется сама, карточка уедет в `Done`

> [!WARNING]
> Не создавать ветку через `git checkout -b` и не переименовывать уже созданную.
> В обоих случаях pull request не свяжется с задачей, она не закроется сама,
> и доска будет врать. Имя ветки задаётся один раз — в диалоге `Create a branch`

> [!TIP]
> Если ветка всё же создана локально, в описание pull request нужно добавить строку
> `Closes #<номер issue>`, иначе задача останется открытой

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
