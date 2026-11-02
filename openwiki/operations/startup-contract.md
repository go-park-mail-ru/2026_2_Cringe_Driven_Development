---
type: guide
title: "Запуск, HTTP-контракт и проверки"
description: "Сборка Go API, миграции, остановка, граница Apidog и команды проверки."
tags: [operations, api, testing]
sources:
  - id: openwiki-source-164e2da859b5277df81c7d94
    resource: repo://.github/workflows/ci.yml
  - id: openwiki-source-0a2b44962b4b8ac526bb9ca5
    resource: repo://build/main.Dockerfile
  - id: openwiki-source-1d795ecce182369dda32d227
    resource: repo://docs/apidog.md
  - id: openwiki-source-a009d602e55f848ea26930dd
    resource: repo://internal/app/app.go
  - id: openwiki-source-5450c45ea05d5ffd1d8ee804
    resource: repo://internal/app/run.go
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-3d8ec5b2c80c2b1c7077f0ce
    resource: repo://internal/config/redis.go
  - id: openwiki-source-55b426b919298d5817843eb0
    resource: repo://internal/transport/http/router.go
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-aff45068e9f0fd7fda322f9c
    resource: repo://migrations/migrations.go
generated: { by: "codex", at: "2026-11-02T21:58:51.903Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-02T21:58:51.903Z
---

# Запуск, HTTP-контракт и проверки

## Запуск и отказ до приёма запросов

`cmd/main` настраивает структурный logger, читает config и вызывает app.Run. Конфигурация требует BFF_API_KEY, JWT_SECRET , S3_NOTEBOOKS_BUCKET, S3_AVATARS_BUCKET и S3_AVATARS_PUBLIC_URL; Redis host/port/db и timeout валидируются отдельно. URL аватаров должен быть абсолютным HTTP(S), без credentials, query и fragment. Остальные RUNTIME_* проверяются при включённом RUNTIME_ENABLED; для openstack обязательны параметры облака, SSH, TLS и имя окружения. Значения секретов и адреса здесь не публикуются: настройку проверяют в `internal/config` и локальной конфигурации окружения.

`newServer` создаёт PostgreSQL pool и Redis client, проверяет PostgreSQL, применяет встроенные goose-миграции и проверяет Redis. Затем собирает SessionStore, security-адаптеры, пользовательский usecase, S3 client, notebook usecase, runtime-модуль, обработку аватаров и роутер. Ошибки раннего запуска закрывают ресурсы и не запускают HTTP listener. Создание клиента S3 не является проверкой доступности бакета: явного пробного запроса к нему в newServer нет.

Run запускает включённый runtime-воркер рядом с HTTP-сервером и ждёт SIGINT/SIGTERM либо ошибку listener. При выходе отменяет воркер и ждёт его завершения до закрытия зависимостей. Подробнее — [runtime-окружения](../architecture/runtime-environments.md). При сигнале даёт HTTP Shutdown десять секунд; при ошибке Shutdown закрывает сервер принудительно. Redis client и PostgreSQL pool освобождаются при выходе. `/health` возвращает alive; обработчик не делает live-проверок PostgreSQL, Redis или S3, поэтому этот ответ не равен готовности всех зависимостей.

## Кто владеет HTTP-контрактом

Канонический контракт принадлежит Apidog. `make generate` запускает cmd/apidog, затем oapi-codegen с `internal/transport/http/api/cfg.yaml`. Конфигурация генератора включает девятнадцать операций auth, профиля и блокнотов. Сгенерированный `api.gen.go` не редактируют руками и не используют как независимый источник этой wiki. Фактическую реализацию ищут в delivery, usecase и router; изменение контракта проходит через процесс из docs/apidog.md. Контракт и реализация должны проверяться вместе, но наличие пункта в Apidog не доказывает реализованный сценарий.

## Локальный цикл

Версию Go берут из go.mod, зависимости — через `go mod download`. `make doctor` проверяет инструменты; `make build` собирает bin/server. `make up` запускает Compose с ожиданием health, `make logs` читает логи, `make down` останавливает окружение. Compose здесь предназначен для разработки; production-конфигурация принадлежит infra.

`make check-fast` проверяет shell-синтаксис, gofmt и go vet. `make check` дополняет это tidy diff, сборкой, тестами и lint. `make test` вызывает scripts/test.sh с race и coverage. `make docker-build` собирает образ по build/main.Dockerfile. Для изменения auth начни с transport/http и user; для хранения — notebook. Это команды проекта, а не заявление, что каждая из них выполнялась при создании wiki.

## Сборка и граница выкладки

`build/main.Dockerfile` собирает server и runtime-smoke, включает артефакты агента и инструменты Ansible/SSH, запускает API непривилегированным пользователем. Это описание упаковки; Python-исходники здесь не исследуются.

В `.github/workflows/ci.yml` docker зависит от lint, test и kernel-images. Конфигурация публикует SHA-тег, а на push main также тег main; latest отключён. Deploy вызывается только для push main исходного репозитория при PRODUCTION_ENABLED=true, получает SHA-тег и через `.github/workflows/cd.yml` запускает deploy-backend.yml из infra/main. Course-sync имеет отдельное условие COURSE_SYNC_ENABLED и не зависит от deploy. Наличие этого YAML не подтверждает, что jobs запускались или завершились успешно.

Проверки runtime-agent отдельные: Python-сервис не входит в этот корпус. Из его наличия нельзя выводить исполнение Go API на kernel. Развёртывание проверяют по infra и конкретному стенду; main, CI и образ сами по себе не являются свидетельством поведения прода.

[Карта Go API](../quickstart.md) · [Auth и Redis](../architecture/auth-sessions.md) · [Блокноты](../architecture/notebooks.md)

## Источники

- [internal/app/app.go#L45-L129](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/app/app.go#L45-L129)
- [migrations/migrations.go#L15-L34](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/migrations/migrations.go#L15-L34)
- [internal/app/run.go#L26-L71](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/app/run.go#L26-L71)
- [internal/transport/http/router.go#L34-L39](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/transport/http/router.go#L34-L39)
- [docs/apidog.md#L1-L7](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/docs/apidog.md#L1-L7)
- [Makefile#L58-L62](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/Makefile#L58-L62)
- [Makefile#L25-L35](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/Makefile#L25-L35)
- [internal/config/config.go#L73-L102](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/config/config.go#L73-L102)
- [internal/config/redis.go#L11-L44](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/config/redis.go#L11-L44)

- [internal/config/runtime.go#L42-L59](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/config/runtime.go#L42-L59)
- [internal/config/runtime.go#L117-L159](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/config/runtime.go#L117-L159)
- [internal/transport/http/api/cfg.yaml#L8-L29](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/transport/http/api/cfg.yaml#L8-L29)
- [build/main.Dockerfile#L8-L36](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/build/main.Dockerfile#L8-L36)
- [.github/workflows/ci.yml#L151-L216](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/.github/workflows/ci.yml#L151-L216)
- [.github/workflows/ci.yml#L230-L255](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/.github/workflows/ci.yml#L230-L255)
- [.github/workflows/cd.yml#L69-L81](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/.github/workflows/cd.yml#L69-L81)
- [scripts/test.sh#L7-L19](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/scripts/test.sh#L7-L19)
