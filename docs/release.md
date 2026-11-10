# CI, публикация и деплой backend

Текущую архитектуру продукта описывает [документация команды](https://docs.cellestial.ru/documentation/architecture/). Здесь — только правила сборки и выпуска backend. Исходники автоматизации: [ci.yml](../.github/workflows/ci.yml), [cd.yml](../.github/workflows/cd.yml) и [infra/ansible](https://github.com/Cringe-Driven-Development-Team/infra/tree/main/ansible).

## Проверки и образ

На каждом PR запускаются `lint` (golangci-lint), `test` (`go mod tidy -diff`, сборка и тесты с race detector) и `docker` (сборка образа без публикации). Job `runtime-agent` проверяет Python-агент и реальные kernels; после него `kernel-images` собирает образ kernel Python. `docker` ждёт `lint`, `test` и `kernel-images`. Покрытие выводится в summary; минимального порога нет. Новый push в PR отменяет предыдущие проверки этого PR. Запуски `main` не отменяют друг друга; выкатки сериализуются отдельно.

После успешных проверок push в `main` публикует образ `ghcr.io/cringe-driven-development-team/backend` с тегами `sha-<short>` и `main`. Тег `latest` не публикуется. SHA-тег может быть перезаписан повторным запуском для того же коммита; для выкатки CI передаёт неизменяемый digest из результата `docker/build-push-action`.

Образы kernels публикуются с тегами `sha-<полный SHA>`; их digest-refs и SHA версии агента передаются в сборку backend. При ручном запуске workflow образ backend получает `sha-<short>` и `pr-<ref>-<short>`, без обновления `main` и без автоматической выкатки. GitHub Releases в этом workflow не создаются.

Backend package должен быть публичным. После каждой публикации (push в `main` или ручной запуск) CI проверяет через GitHub API видимость `public` и привязку package к командному репозиторию, затем выполняет анонимный `docker manifest inspect` именно опубликованного digest с пустым Docker config. При ошибке CD не запускается. PR CI только собирает образ и не проверяет доступность опубликованного package.

CI публикует образ через `GITHUB_TOKEN` с `packages: write`. CD не требует `packages: read` и не передаёт GHCR credentials в Ansible. Перед первой реальной выкаткой владелец package должен переключить его видимость в Public; изменение workflows само по себе видимость не меняет. При 401/403 передайте ментору ссылку на запуск для проверки доступа к package; личные токены для обхода не используйте.

## Учебная копия

Рабочие PR, задачи и ревью находятся в [командном backend](https://github.com/Cringe-Driven-Development-Team/backend). [Учебный репозиторий](https://github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development) получает те же коммиты `main`; Actions и новые PR там выключены.

После `lint`/`test`/`docker` синхронизация запускается при `COURSE_SYNC_ENABLED=true` независимо от деплоя. Для повтора без деплоя используйте Actions → Sync to course → `main` и передайте `ci_run_id` успешного push/main CI. Подробный порядок восстановления — в [runbook организации](https://github.com/Cringe-Driven-Development-Team/.github/blob/main/docs/migrations/team-repositories-runbook.md).

## Production

После успешных проверок `ci.yml` вызывает `cd.yml` через `workflow_call` и передаёт полный `backend_image_ref=ghcr.io/cringe-driven-development-team/backend@sha256:<64 hex>` из job `docker`. CD проверяет формат ref и передаёт его в Ansible; теги не принимаются. Выкатка запускается при `PRODUCTION_ENABLED=true` из `main` в environment `production`. CD запускает `deploy-backend.yml` из `infra/main`; production Compose, Caddyfile, Vault и серверная конфигурация принадлежат репозиторию [infra](https://github.com/Cringe-Driven-Development-Team/infra). Местный `docker-compose.yml` служит только для разработки.

`DEPLOY_SSH_KEY`, `SSH_KNOWN_HOSTS` и `ANSIBLE_VAULT_PASSWORD` хранятся как secrets environment. Для командного CI установлен отдельный public key `infra/ansible/files/authorized_keys/ci-team-backend.pub`; личные SSH-ключи и Selectel credentials в CI не передаются. Ansible скачивает выбранный digest анонимно с временным пустым Docker config и удаляет этот config после попытки скачивания.

Playbook ждёт healthy-контейнер API и проверяет внутренний `/health` с gateway: ожидаются HTTP 200 и `status: alive`. Цепочка BFF → Go проверяется через HTTPS `POST /api/trpc/auth.login` со случайным логином и фиктивным паролем: ожидаются HTTP 400, `error.data.code: BAD_REQUEST` и `error.data.appCode: invalid_credentials`. Публичные `/api/v1/users/me`, `/api/unknown` и `/health` должны возвращать HTTP 404 без HTML. Проверки описаны в [probe-backend.yml](https://github.com/Cringe-Driven-Development-Team/infra/blob/main/ansible/roles/app/tasks/probe-backend.yml) и [probe-bff.yml](https://github.com/Cringe-Driven-Development-Team/infra/blob/main/ansible/roles/app/tasks/probe-bff.yml). Миграции goose выполняются API при старте; отдельного шага миграций нет. Ошибка Telegram показывается предупреждением и не подменяет результат деплоя.

Блокноты хранятся в PostgreSQL. S3 используется для аватаров: роль `app` передаёт из infra vars и Vault `S3_AVATARS_BUCKET`, `S3_AVATARS_PUBLIC_URL`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` и `AWS_ENDPOINT_URL_S3`. Локально используется SeaweedFS, в production — Selectel. Старый мигратор ipynb и его конфигурация удалены; вместо переноса данных используется [однократный сброс БД](database-reset.md). Приложение больше не использует бакет блокнотов; его удаление не выполняется этим кодом.

Откат выполняется из infra с прежним опубликованным digest через `backend_image_ref` по [инструкции Ansible](https://github.com/Cringe-Driven-Development-Team/infra/blob/main/ansible/README.md). Миграции Down автоматически не запускаются. Перед откатом сверяйте digest с исходным запуском публикации и проверяйте, что выбранный бинарник работает с текущей схемой БД.

## Выпуск перехода на Redis

Job `test` поднимает настоящий Redis 8 и проверяет TTL, атомарную ротацию,
отзыв, гонки, потерю ответа и восстановление данных. Изолированный restart-тест
проверяет AOF. Перед первым выпуском остановите старый API и следуйте
[runbook](redis-sessions.md): старые refresh-сессии очищаются, новый JWT требует
sid/версии. Откат на stateless API требует смены JWT-ключа и удаления старого
verification key; одного очищения refresh недостаточно.
