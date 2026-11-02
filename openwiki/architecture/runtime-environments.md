---
type: architecture
title: "Жизненный цикл runtime-окружений в Go"
description: "Сервис окружений, очередь PostgreSQL, lease воркера, создание и очистка ресурсов через адаптеры."
tags: [runtime, worker, postgres, provisioning]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-02T22:04:01.439Z
sources:
  - id: openwiki-source-a009d602e55f848ea26930dd
    resource: repo://internal/app/app.go
  - id: openwiki-source-5450c45ea05d5ffd1d8ee804
    resource: repo://internal/app/run.go
  - id: openwiki-source-08f73a6cbd452c1a6a0a9454
    resource: repo://internal/runtime/adapters/openstack/provider.go
  - id: openwiki-source-80527b1b02b4c10f5f035479
    resource: repo://internal/runtime/adapters/postgres/environments.go
  - id: openwiki-source-f08e86c5293b0d4d83463492
    resource: repo://internal/runtime/adapters/postgres/jobs.go
  - id: openwiki-source-5caedd671d86311b3df2dd43
    resource: repo://internal/runtime/usecase/create.go
  - id: openwiki-source-600f480336937ddaa0150e0d
    resource: repo://internal/runtime/usecase/delete.go
  - id: openwiki-source-7c5b2622482ae158ab24f724
    resource: repo://internal/runtime/usecase/reconcile.go
  - id: openwiki-source-cdaba5d803745f298794e37d
    resource: repo://internal/runtime/usecase/service.go
  - id: openwiki-source-080f23788565faaa3a6f0817
    resource: repo://internal/runtime/usecase/worker.go
generated: { by: "codex", at: "2026-11-02T22:04:01.439Z" }
---

# Жизненный цикл runtime-окружений в Go

## Граница интеграции

`internal/app` вызывает runtime.Build и при включённом модуле запускает Worker.Run рядом с HTTP-сервером. EnvironmentService сохранён в application, но не передан HTTP-фасаду: этот SHA не подключает создание окружений к обработчикам блокнотов. `cmd/runtime-smoke` — отдельная ручная точка входа. Самостоятельный Python-сервис и роли infra вне корпуса; описание ниже подтверждает Go-код, а не фактическую настройку VPS или работу облака.

При RUNTIME_ENABLED=false Build возвращает сервис с отключённым RequestCreate и nil-воркер, не создавая облачные адаптеры; Get и RequestDelete по-прежнему обращаются к БД. При включении выбираются fake либо openstack; fake подставляет локальные адаптеры облака, настройки хоста и агента. Конфигурация и необходимые файлы задаются `internal/config/runtime.go` и `internal/runtime/module.go`, без публикации значений секретов и адресов.

## Запрос и очередь

RequestCreate проверяет SessionID, язык и профиль и сохраняет окружение QUEUED с desired_state=READY вместе с заданием create. PostgreSQL использует общую advisory-блокировку при проверке лимита активных окружений. Повтор с тем же SessionID возвращает существующее окружение; другой язык или профиль даёт конфликт. Миграция `migrations/20261030130000_create_runtime.sql` хранит окружения и задания и ограничивает активные задания одним на сессию.

Claim выбирает готовое задание без действующего lease через FOR UPDATE SKIP LOCKED. Воркер ограничивает конкурентность и продлевает lease; потеря владельца или отсутствие успешного продления в течение lease отменяет текущие шаги. Перевод задания в delete отменяет создание. Записи состояния проверяют версию окружения и владельца работающего задания; desired_state=DELETED не затирается старым созданием.

## Создание и проверка готовности

Последовательность состояний: QUEUED → CREATING → CONFIGURING → VERIFYING → READY. В CREATING обеспечиваются порт, том и сервер. Полученный ID сохраняется даже вместе с ошибкой облака; Ensure вызывается повторно и для известного ID, чтобы завершить ожидание готовности. Ключ хоста и токен сохраняются в запечатанном виде до использования. CONFIGURING вызывает HostConfigurator, VERIFYING опрашивает AgentClient.Health и сверяет HTTP-статус, SessionID, язык и Ready.

Общий срок создания отсчитывается от CreatedAt, отдельные вызовы health также ограничены. Постоянная ошибка облака или истечение срока при создании переводит окружение в CLEANUP_PENDING; после очистки итог — FAILED. Временные ошибки повторяются с backoff.

## Удаление и восстановление

Удаление пробует Drain/Stop агента, затем удаляет сервер, том и порт. Недоступность агента не блокирует удаление. Ресурсы берутся и из сохранённых ID, и из метаданных облачных ресурсов. Обычное успешное удаление заканчивается DELETED. Постоянная ошибка удаления сохраняет CLEANUP_PENDING и завершает текущее задание; reconcile ставит очистку снова.

Reconcile запускается при старте воркера и периодически: повторяет CLEANUP_PENDING и удаляет ресурсы неизвестных либо завершённых сессий. ListOwned ограничивает выборку меткой окружения, ресурсы без SessionID не удаляются этим проходом. Сам проход имеет timeout; просроченные lease подхватывает Claim.

## Адаптеры и проверки

Go-клиент агента использует mTLS с заданным CA, Bearer-токен и отключённый proxy из окружения. Ansible runner создаёт временный каталог операции с правами 0700, файлы inventory/vars/known_hosts с 0600 и включает StrictHostKeyChecking по заранее известному ключу. Он запускает подготовленный playbook; содержимое ролей infra здесь не исследуется.

`internal/runtime/usecase/worker_test.go` проверяет продолжение создания, потерю lease, удаление во время настройки, чужую сессию/язык health, timeout и очистку сирот. `adapters/postgres/*_test.go` проверяют очередь и записи состояния; `module_test.go` — сборку зависимостей. Эти тестовые сценарии не являются свидетельством запуска в облаке.

[Карта Go API](../quickstart.md) · [Запуск и конфигурация](../operations/startup-contract.md)

## Источники
- [internal/app/app.go#L35-L42](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/app/app.go#L35-L42)
- [internal/app/app.go#L92-L111](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/app/app.go#L92-L111)
- [internal/app/run.go#L36-L52](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/app/run.go#L36-L52)
- [internal/runtime/module.go#L66-L120](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/module.go#L66-L120)
- [internal/runtime/usecase/service.go#L40-L82](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/service.go#L40-L82)
- [internal/runtime/adapters/postgres/environments.go#L35-L67](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/adapters/postgres/environments.go#L35-L67)
- [internal/runtime/adapters/postgres/environments.go#L86-L126](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/adapters/postgres/environments.go#L86-L126)
- [internal/runtime/adapters/postgres/jobs.go#L13-L63](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/adapters/postgres/jobs.go#L13-L63)
- [internal/runtime/usecase/worker.go#L150-L205](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/worker.go#L150-L205)
- [internal/runtime/usecase/create.go#L14-L123](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/create.go#L14-L123)
- [internal/runtime/usecase/create.go#L210-L279](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/create.go#L210-L279)
- [internal/runtime/usecase/delete.go#L19-L123](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/delete.go#L19-L123)
- [internal/runtime/usecase/reconcile.go#L14-L102](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/usecase/reconcile.go#L14-L102)
- [internal/runtime/adapters/agent/client.go#L40-L95](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/adapters/agent/client.go#L40-L95)
- [internal/runtime/adapters/ansible/runner.go#L120-L175](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/runtime/adapters/ansible/runner.go#L120-L175)
