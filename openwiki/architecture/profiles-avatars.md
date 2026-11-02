---
type: architecture
title: "Профиль, пароль и аватары"
description: "Изменение профиля, конкурентная смена пароля, обработка изображения и публикация ссылки на аватар."
tags: [users, profile, avatar, security]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-02T21:58:51.903Z
sources:
  - id: openwiki-source-7f2f2eb724b9a17ee724de12
    resource: repo://internal/transport/http/middleware/avatar.go
  - id: openwiki-source-406983d5775b589a9813a316
    resource: repo://internal/user/adapters/avatar/codec.go
  - id: openwiki-source-7058e75a6d40ff7caa7c3db3
    resource: repo://internal/user/adapters/postgres/user.go
  - id: openwiki-source-a30a6c6605ee1a9b1895e42e
    resource: repo://internal/user/delivery/http/profile.go
  - id: openwiki-source-ac561a30179aedee23f77bf6
    resource: repo://internal/user/usecase/avatar.go
  - id: openwiki-source-6efcd93307a3ac095092d4c5
    resource: repo://internal/user/usecase/profile.go
generated: { by: "codex", at: "2026-11-02T21:58:51.903Z" }
---

# Профиль, пароль и аватары

## Профиль и отзыв сессий

Delivery получает ID текущего пользователя из principal, а usecase работает через UserStore. Логин допускает 3–32 латинские буквы, цифры и подчёркивание; конфликт уникальности PostgreSQL превращается в ErrLoginTaken и HTTP 409. Ссылку на изображение delivery формирует из S3_AVATARS_PUBLIC_URL и avatar_key; миграция `migrations/20261102120000_add_user_avatar.sql` добавляет nullable avatar_key.

Смена пароля проверяет различие старого и нового пароля, UTF-8, 8–72 символа и не более 72 байт. После проверки текущего хеша выполняется CompareAndSwapPassword с условием на старый хеш, затем RevokeAll в Redis. Это две отдельные записи: если отзыв сессий завершился ошибкой, пароль уже мог измениться. Порядок выдачи сессии при конкурентной смене пароля описан в [авторизации](auth-sessions.md).

## Путь загрузки

После S2S и Bearer middleware для PUT аватара требует principal и multipart/form-data, ограничивает полное HTTP-тело шестью МиБ и пропускает одну загрузку одновременно на экземпляр роутера. Delivery принимает ровно один файл в поле avatar, JPEG либо PNG, не более пяти МиБ, и проверяет отсутствие дополнительных частей и превышения лимита в эпилоге. Исходный файл хранится во временном файле, который удаляется после обработки.

Codec проверяет соответствие MIME фактической сигнатуре и размеры от 200 до 4096 пикселей по каждой стороне до полного декодирования. Повторное кодирование удаляет метаданные изображения; прозрачность PNG накладывается на белый фон, JPEG кодируется с качеством 85. Очищенный временный файл закрывается и удаляется после загрузки. Ошибки размера, типа и содержимого отображаются delivery в 413, 415 и 400 соответственно.

## Публикация и сбои

AvatarUsecase проверяет наличие пользователя, очищает изображение и загружает новый UUID-объект в отдельный бакет аватаров. SwapAvatar под SELECT FOR UPDATE меняет avatar_key и возвращает прежний ключ после commit. Только после успешного переключения ссылки удаляется старый объект. Ошибка удаления логируется и не отменяет успешную загрузку.

При ошибке SwapAvatar новый объект не удаляется: неуспешный ответ commit может скрывать выполненную запись. Это может оставлять лишние объекты; общей транзакции PostgreSQL/S3 нет. S3-адаптер ограничивает Put/Delete пятью секундами и одной попыткой, передавая файл потоком. Конфигурация публичного URL и бакета находится в `internal/config/config.go`; наличие этих настроек не подтверждает доступность CDN или облака.

## Проверки и связи

`internal/user/usecase/profile_test.go` проверяет валидацию, CAS и отказ Redis после смены пароля. `avatar_test.go` проверяет публикацию и очистку при сбоях; `internal/user/adapters/avatar/codec_test.go` — формат, размеры и прозрачность. `internal/transport/http/profile_test.go` проверяет цепочку HTTP, отзыв сессий и загрузку.

[Карта Go API](../quickstart.md) · [Запуск и контракт](../operations/startup-contract.md)

## Источники
- [internal/user/usecase/profile.go#L11-L46](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/usecase/profile.go#L11-L46)
- [internal/user/adapters/postgres/user.go#L94-L147](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/adapters/postgres/user.go#L94-L147)
- [internal/transport/http/middleware/avatar.go#L22-L56](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/transport/http/middleware/avatar.go#L22-L56)
- [internal/user/delivery/http/profile.go#L76-L148](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/delivery/http/profile.go#L76-L148)
- [internal/user/adapters/avatar/codec.go#L45-L128](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/adapters/avatar/codec.go#L45-L128)
- [internal/user/usecase/avatar.go#L50-L77](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/usecase/avatar.go#L50-L77)
- [internal/user/adapters/s3/storage.go#L15-L57](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/user/adapters/s3/storage.go#L15-L57)
