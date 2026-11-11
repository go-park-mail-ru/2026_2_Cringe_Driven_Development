# Выпуск и ручной откат backend

CI и выкатка принадлежат командному backend. Учебный репозиторий — копия main,
без рабочих PR и Actions. Production Compose/Vault/SSH находятся в infra,
карточки и доставка Telegram — в `.github/services/webhook-alerts` (.github#59).

## Публикация

CI проверяет lint/test/docker и публикует backend через `GITHUB_TOKEN` с
`packages: write`. Теги: `sha-<полный 40-hex SHA>` и `main`, без `latest`.
SHA-тег может измениться при повторной сборке: выкатка получает digest из
`docker/build-push-action`, а не разрешает тег повторно.

Package `ghcr.io/cringe-driven-development-team/backend` должен быть Public.
После каждого publish CI проверяет public visibility, привязку к repository и
анонимный pull точного digest для linux/amd64 с чистым временным Docker config.
Public разрешает скачивание, но публикация новых образов требует write.

Перевод package в Public — отдельное действие владельца в package settings
после подготовки backend/infra PR и непосредственно перед применением infra#69.
Вернуть публичный package в Private нельзя (см. GitHub Packages visibility docs).
Если infra#69 уже применён, подтвердить фактическую public visibility и точный
анонимный pull перед общей приёмкой; наличие кода проверки не доказывает её успех.
Deploy/Rollback GHCR token не получают, login/logout на VPS не выполняется.

## Production: один путь для выкатки и отката

Push/main CI вызывает reusable `cd.yml`; `Rollback` — отдельный
`workflow_dispatch` на main, вызывающий тот же CD. Нужны
`PRODUCTION_ENABLED=true` и approval/rules environment `production`.
Environment secrets сохранены, автоматический Deployment выключен через
`deployment: false`: producer создаёт одну явную запись на операцию/attempt.
Job получает `deployments: write`, `contents: read`, `actions: read`.

Deploy и rollback сериализуются группой `backend-production`, без отмены
выполняющегося запуска. Выбор истории/базы делается внутри этой группы.
Перед SSH проверяются источник, полный SHA, backend package/digest и доступность
образа анонимно. Неизвестная/неоднозначная цель отклоняется до VPS.

Используется `infra/ansible/deploy-backend.yml` из infra/main с
`backend_image_ref=ghcr.io/cringe-driven-development-team/backend@sha256:<64 hex>`.
Нужны `DEPLOY_SSH_KEY`, `SSH_KNOWN_HOSTS`, `ANSIBLE_VAULT_PASSWORD` environment;
сохраняются SSH jump через gateway, StrictHostKeyChecking и очистка временных файлов.
Go остаётся на приватной VPS2; runner к её HTTP напрямую не обращается.

`Switch backend` выполняет существующий playbook один раз. Allowlisted Ansible
callback фиксирует подтверждённый выбранный образ/healthy контейнер, внутреннюю
проверку и внешние probes; не анализирует человекочитаемый stdout и не сохраняет
секреты. `Health check` отдельным шагом требует полного набора свидетельств;
exit 0 команды сам по себе не является успехом. Если callback не сработал,
проверка остаётся неизвестной и Deployment не становится success.

Обязательные проверки infra#69:

- VPS2: image ID работающего api совпадает с `.Id` образа по выбранному digest,
  контейнер running/healthy. Локальный image ID не сравнивается напрямую с GHCR digest.
- Gateway → private IP VPS2:8080: GET /health → 200 и JSON status=alive.
- Runner → HTTPS BFF: отрицательный POST /api/trpc/auth.login со свежим случайным
  32-hex login, фиктивным паролем, Origin/X-CSRF/Content-Type; без cookie/аккаунта.
- Публичные `/api/v1/users/me`, `/api/unknown`, `/health`: 404 без HTML приложения.

**Уточнение действующего контракта:** актуальный infra/main ожидает для login
`400`, `error.data.code=BAD_REQUEST`, `error.data.appCode=invalid_credentials`.
Старый текст backend#42/infra#69 указывает 401/UNAUTHORIZED. Producer не дублирует
HTTP-логику и использует фактический общий probe Ansible. Расхождение необходимо
согласовать при общей приёмке; менять статусы GitHub issues этот код не пытается.
Внутренний и внешний login probe имеют максимум 6 запросов, timeout 5 s, delay 5 s.
Дополнительные проверки guest HTML/users.me не заменяют отрицательный login.
Эти проверки не доказывают успешную сессию пользователя, Redis или runtime-VPS.

## Выбор версии для Rollback

Откройте backend → Deployments → production. Успешная запись содержит полный
SHA и `payload.image_ref`; проверьте нужный package и неизменяемый digest.
Это последний подтверждённый результат тогда, а не доказательство работы VPS сейчас.
Предварительное SSH-чтение Docker не требуется.

Actions → **Rollback** → **Run workflow** → ветка **main**:

- SHA пустой: после последней success выбирается предыдущая success с другим
  digest; после failure/error — последняя подтверждённая success для восстановления.
- Последняя запись незавершённая/неизвестная, история недоступна или нет подходящего
  успеха: автоматический выбор запрещён.
- Можно указать полный SHA конкретного прежнего успешного Deployment. Все его
  успешные записи должны содержать один digest; разные digest — отказ.
  Явный SHA позволяет восстановиться при неизвестной последней попытке.

Не передавайте digest из старого сообщения Telegram или изменяемого тега.
После переключения выполняются те же проверки, что для deploy.
**Миграции goose применяются при старте API; rollback не выполняет Down и не
восстанавливает данные.** Совместимость старого бинарника с применённой схемой
нужно оценить заранее; откат схемы/данных не входит в #42. Автоматического отката нет.

## Deployment, события и Summary

Контракт общий с .github#59:

- repository — Cringe-Driven-Development-Team/backend, environment — production;
- ref и разрешённый sha — целевой полный SHA; task=deploy|rollback;
- payload.action=task, image_ref — полный digest-ref; payload.run — текущий run;
- notification.run_attempt — положительный integer; base_sha — последний
  подтверждённый успешный production SHA перед операцией либо null;
- notification.report_url — `<текущий run>#summary`;
- status содержит environment_url=https://cellestial.ru, log_url текущего attempt;
- один Deployment на операцию/attempt, in_progress до доступа на переключение,
  затем success только после всех обязательных проверок, иначе failure/error.

Терминальный deployment_status — основной результат. Ошибка/отмена до Deployment
или итогового status — workflow_job.completed fallback, общий semantic key
repository + action + run_id + run_attempt. Backend не публикует release-artifacts,
frontend assets не нужны. Consumer читает проверенный run/attempt и steps через API.
Фактический deploy job — `deploy / deploy`; целевое имя rollback — `rollback / deploy`,
его необходимо подтвердить Jobs API при первом разрешённом запуске, без пробного
production rollback ради fixture.

Summary каждой исполняемой попытки содержит действие, target/ref, базовый релиз,
инициатора, этап ошибки, отдельные switch/image/internal/external/routes результаты,
ссылки на логи, коммиты/PR/задачи. При rollback перечисляются снимаемые изменения
(target..base). Если история не полностью доступна, это явно отмечается; результат
операции не скрывается. При потере runner полный Summary может не дописаться —
fallback не должен придумывать отсутствующие свидетельства.

Если switch не подтверждён — состояние неизвестно. Если подтверждён, а проверка
не прошла — есть факт выбранной версии на этапе переключения, Health Check не
успешен; текущее состояние при утрате доступа неизвестно. Историческая base_sha
никогда не подставляется как фактическая версия. Поздний сбой после обязательных
проверок не переписывает успешный результат; сбой записи status требует сверки
по шагам/Summary и не создаёт новый Deployment автоматически.

Результат операции диагностируется в Actions/Deployments. Доставка карточки —
в webhook logs/outbox, независимо от Actions; Telegram-сбой не меняет результат
выкатки. Прямых Telegram steps/credentials в CD нет. Кнопка «↩️ Откатить» только
открывает Rollback workflow, не выполняет операцию и не сохраняет цель из сообщения.

## Локальные проверки и общая приёмка

`make release-check` — Python standard-library tests с fake GitHub/processes;
`make check`, `make docker-build` — обязательные backend проверки;
`actionlint .github/workflows/{ci,cd,rollback}.yml` — workflow lint.
Fixtures в `scripts/release/tests/fixtures/backend-release-contract.json` совпадают
с consumer .github#59; сценарии rerun, отказов и health ошибок проверяются локально,
без сообщений в чат и без пробного отката production.

Код можно подготовить независимо от #59 и infra#64. После готовности обеих сторон
проверить runtime конфигурацию без выключенного пути и на штатной выкатке цепочку
«published digest → VPS2 → checks → Deployment → webhook → карточка».
До полной приёмки backend PR пишет `Part of` на #42; закрывать родителя можно
только после подзадач и интеграции. Публикация/доступы/production применяются
отдельно владельцем, не локальными тестами.

Course-sync остаётся независимым от deploy, с `COURSE_SYNC_ENABLED=true`.
Повторяется через ci_run_id согласно runbook организации, без повторной выкатки.
