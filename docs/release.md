# Выпуск, выкатка и откат backend

Опубликованный immutable GitHub Release означает «выпуск подготовлен».
Успешный production Deployment означает «выпуск выкачен и прошёл проверки».
CI и CD имеют отдельные Actions runs; успешный CI не подтверждает состояние VPS.
Production Compose/Vault/SSH принадлежат infra, доставка уведомлений —
`.github/services/webhook-alerts` (.github#59). Прямой отправки Telegram из backend нет.

## Подготовка репозитория

В Settings → General → Releases включить **Enable release immutability** до первого
выпуска. Администратор может выполнить:

```sh
gh api -X PUT repos/Cringe-Driven-Development-Team/backend/immutable-releases
gh api repos/Cringe-Driven-Development-Team/backend/immutable-releases
```

Для этих endpoints нужны Administration write/read; `GITHUB_TOKEN` CI их не имеет.
CI проверяет `immutable: true` у опубликованного выпуска; при отсутствии этого
признака не запускает CD. Уже опубликованный mutable Release автоматически не
заменяется. Нельзя считать локальные тесты подтверждением настройки репозитория.

GHCR package `ghcr.io/cringe-driven-development-team/backend` должен быть Public
и привязан к backend. CI проверяет настройки package и анонимный pull точного
`linux/amd64` digest с пустым Docker config. Публикация требует `packages: write`,
Release — `contents: write`; CD/Ansible GHCR credentials не получают.

## Автоматическая подготовка выпуска

Только push в `main` после обязательных проверок публикует production Release
`release-<полный 40-hex SHA>`. PR и ручной CI не публикуют backend production-выпуск.
Образ имеет теги `sha-<SHA>` и `main`, без `latest`; источник выкатки — digest из manifest.
Архив получается через pull опубликованного digest → docker save → gzip, без второй сборки.
Перед публикацией архив загружается через docker load с проверкой image ID.

Release сначала создаётся draft. После загрузки и повторной проверки файлов
публикуется immutable Release с generated changelog, PR/compare ссылками, SHA,
ссылкой на исходный CI run и командой скачивания по digest. Файлы:

- `backend-image.tar.gz`: Docker-образ для самостоятельной загрузки;
- `release.json`: `format_version: 1`, `source_sha`, `image_ref`, `ci_url`,
  `build.platform`, `build.image_id`, `archive.name`, `archive.size`, `archive.sha256`;
- `SHA256SUMS`: SHA256 архива и точных байтов `release.json`.

Автоматические GitHub source archives не являются сборочными артефактами.
Скачивание файлов: Release → Assets либо `gh release download <tag> -R
Cringe-Driven-Development-Team/backend`. Затем:

```sh
sha256sum -c SHA256SUMS
docker load -i backend-image.tar.gz
```

При повторе CI опубликованный выпуск полностью проверяется и используется без
пересборки backend или изменения файлов. Публикации одного SHA сериализованы.
Если загрузка оборвалась, draft с `release.json` фиксирует digest: повтор берёт
именно его, восстанавливает архив и проверяет соответствие checkpoint перед
добавлением отсутствующих файлов. Несогласованные файлы не перезаписываются.
Если checkpoint ещё не загружен и опубликованного выпуска нет, повтор может собрать
образ заново; завершённый Release никогда не пересобирается.

## Самостоятельный CD и повторное применение infra

После публикации CI вызывает `workflow_dispatch` workflow `CD` на `main`, передавая
конкретный `release_tag`. Вызывающая job имеет только `contents: read`, `actions: write`.
На `release: published` автоматизация не полагается: публикация через `GITHUB_TOKEN`
не запускает следующий workflow. Ошибка dispatch — отдельная ошибка CI job;
Release остаётся готовым, его можно выкатить вручную без пересборки.

Ручной путь: Actions → **CD** → **Run workflow** → branch `main` → обязательный
`release_tag`, например `release-0123456789012345678901234567890123456789`.
У CD собственный run, история и Summary. Разрешены текущий подтверждённый выпуск
и новый выпуск, происходящий от текущего. Старую версию выбирают через Rollback.
Внутри общей production concurrency-секции CD заново читает Deployments: запоздавший
запуск предка не откатывает более новую успешную выкатку. Расходящаяся история
и неизвестное последнее состояние отклоняются, требуют сверки истории и состояния.

До доступа к SSH проверяются публикация/immutability, точный tag и его commit,
manifest, контрольные суммы скачанных файлов, разрешённый backend package/digest
и анонимный pull. Draft, отсутствующий выпуск, несогласованные SHA/digest/checksum
отклоняются с причиной в логе. Dispatch HEAD не определяет образ.

Каждый CD получает актуальную `infra/main`, пишет её SHA в Summary и передаёт
`backend_image_ref=ghcr.io/cringe-driven-development-team/backend@sha256:<64 hex>`
в `infra/ansible/deploy-backend.yml`. Штатная выкатка скачивает образ из GHCR;
архив на VPS не загружается. Повторный CD текущего Release снова запускает playbook,
применяя свежие переменные, включая `runtime_extra_env`. Infra шаблонизирует их
в `.env` и Compose, а Compose пересоздаёт API при изменении окружения.
Совпадение digest не является условием пропуска playbook.

### Как найти текущий подтверждённый Release

Открыть backend → Environments → production → последний **успешный Deployment**.
Перейти по его log URL в CD/Rollback run и прочитать Summary: target SHA,
`release-<SHA>`, image digest и infra SHA. Этот tag указать в CD для повторной выкатки.
Последний по дате опубликованный Release и текущий HEAD не подтверждают выкатку.
История GitHub также не утверждает текущее состояние VPS после потери доступа.

## Rollback и обязательные проверки

Actions → **Rollback** → **Run workflow** на `main`. Необязательное поле `sha` —
полный SHA ранее успешно выкаченной версии. Без поля: после успешного последнего
запуска выбирается предыдущий успешный другой digest; после failure/error — последний
успешный. Неизвестное или незавершённое последнее состояние запрещает автоматический
выбор; явная цель тоже требует успешного подтверждения и однозначного digest.

Цель должна иметь опубликованный immutable Release с тем же digest. Старые записи
CI #102 остаются свидетельствами истории, но не заменяют отсутствующий Release.
Импорт старых выпусков не выполняется. Откат не пересобирает образ и не откатывает БД.
CD, повторная выкатка и Rollback используют одну группу `backend-production`,
не отменяют выполняющуюся операцию; выбор цели происходит внутри этой группы.

Сохраняются `PRODUCTION_ENABLED=true`, environment `production`, доверенные workflow
на `main`, SSH host-key verification и удаление временных SSH/Vault файлов.
Deployment получает `in_progress`, затем terminal status только на основании фактов:
фактический digest и healthy контейнер, внутренний Go API с gateway, внешняя цепочка
BFF → Go → PostgreSQL и закрытость публичных Go-маршрутов. Неподтверждённые этапы
остаются unknown/not_run. После обязательных проверок поздняя ошибка отчётности не
переписывает успех самих проверок; ошибка регистрации статуса видна отдельно.

## Webhook-контракт и интеграционная проверка

Payload сохраняет `action`, `image_ref`, `run`, `notification.run_attempt`,
`notification.base_sha`, `notification.report_url`; добавлены `release_tag`, `infra_sha`.
CD: workflow `cd.yml`, event `workflow_dispatch`, job `deploy`; Rollback сохраняет
`rollback.yml`, `workflow_dispatch`, job `rollback / deploy`. Имена шагов
`Switch backend` и `Health check` сохраняются. Target SHA независим от dispatch HEAD.

Текущий consumer .github#59 требует сопутствующего обновления распознавания CD.
Подготовленный patch: [release-webhook.patch](release-webhook.patch), проверен на
`.github/main` SHA `5eb65898cfd4b980f20c0cb3d3d8f61f122b1ea1`. Применять
в checkout `.github` через `git apply /path/to/backend/docs/release-webhook.patch`;
он добавляет CD в разрешённые источники и job fallback, сохраняя проверки ветки,
репозитория, workflow ID, попытки и доказательств выполнения. Patch содержит тесты
и общий fixture отдельного CD с HEAD, отличным от target. До применения consumer
новые CD-уведомления не считаются интеграционно принятыми. Telegram sender не меняется.

Локально: `make release-check`, `actionlint .github/workflows/{ci,cd,rollback}.yml`,
`make check`, `make docker-build`. Consumer patch проверяется `bun test` в
`services/webhook-alerts` без сетевой отправки уведомлений. На интеграционной приёмке
приложить ссылки на настоящий CI run, Release и отдельный CD run; проверить повтор
текущего Release с изменением infra при том же digest и всю цепочку Deployment → webhook.
Пробный production Rollback ради локальной приёмки не требуется.

Course-sync независим от CD, управляется `COURSE_SYNC_ENABLED=true` и повторяется
по `ci_run_id` без повторной выкатки.
