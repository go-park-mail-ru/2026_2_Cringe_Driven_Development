# ER-диаграмма ДЗ №1

Десять SQL-таблиц находятся в PostgreSQL. Refresh-сессии — в Redis,
содержимое ipynb, файлов окружения и аватаров — в S3. Диаграмма редактируется непосредственно
в этом Markdown-файле и использует стандартный блок Mermaid `erDiagram`.

`PK` — первичный ключ, `FK` — внешний ключ, `UK` — уникальный атрибут.
Составные и частичные ключи описаны в [relations.md](relations.md).
`||` — ровно один, `o|` / `|o` — ноль или один, `o{` — ноль или много.
Пунктир обозначает неидентифицирующие связи: FK не входит в PK дочерней таблицы.
Связи с Redis/S3 логические, не SQL FOREIGN KEY; они подписаны отдельно.

SQL-типы не указываются. Для обязательной пары `type name` в синтаксисе Mermaid
используется технический маркер `attr`, не обозначающий тип данных БД.

```mermaid
erDiagram
    app_user {
        attr id PK
        attr avatar_image_id FK
        attr login UK
        attr password_hash
        attr created_at
        attr updated_at
    }
    project {
        attr id PK
        attr user_id FK
        attr notebook_file_id FK, UK
        attr name
        attr created_at
        attr updated_at
    }
    environment_file {
        attr id PK
        attr project_id FK
        attr file_id FK, UK
        attr path
        attr created_at
        attr updated_at
    }
    file {
        attr id PK
        attr file_path UK
        attr file_extension
        attr created_at
        attr updated_at
    }
    runtime_session {
        attr id PK
        attr project_id FK
        attr start_time
        attr end_time
        attr created_at
        attr updated_at
    }
    subscription_type {
        attr id PK
        attr name UK
        attr runtime_limit
        attr ram_limit
        attr cpu_limit
        attr price_rub
        attr duration
        attr created_at
        attr updated_at
    }
    user_subscription {
        attr id PK
        attr user_id FK
        attr subscription_type_id FK
        attr start_time
        attr end_time
        attr created_at
        attr updated_at
    }
    promo_code {
        attr id PK
        attr code UK
        attr expires_at
        attr created_at
        attr updated_at
    }
    payment {
        attr id PK
        attr user_id FK
        attr subscription_type_id FK
        attr promo_code_id FK
        attr created_at
        attr updated_at
    }
    project_share {
        attr id PK
        attr project_id FK
        attr user_id FK
        attr created_at
        attr updated_at
    }
    refresh_session["Redis: refresh_session"] {
        attr id PK
        attr user_id
        attr token_hash UK
        attr expires_at
        attr created_at
    }
    s3["S3: ipynb, environment files, avatar"]

    file |o..o{ app_user : "avatar_image_id"
    app_user ||..o{ project : "user_id"
    file ||..o| project : "notebook_file_id"
    project ||..o{ environment_file : "project_id"
    file ||..o| environment_file : "file_id"
    project ||..o{ runtime_session : "project_id"
    app_user ||..o{ user_subscription : "user_id"
    subscription_type ||..o{ user_subscription : "subscription_type_id"
    app_user ||..o{ payment : "user_id"
    subscription_type ||..o{ payment : "subscription_type_id"
    promo_code |o..o{ payment : "promo_code_id"
    project ||..o{ project_share : "project_id"
    app_user ||..o{ project_share : "user_id"
    app_user ||..o{ refresh_session : "user_id (logical)"
    file ||..|| s3 : "file_path (logical)"
```

Проект может не иметь файлов окружения; путь файла уникален внутри проекта.
Один пользователь может иметь много проектов, подписок, платежей, полученных
доступов и refresh-сессий. Платёж может быть без промокода.
Для проекта хранится много запусков; одновременно незавершённым может быть один.
Подписки пользователя не пересекаются по времени.

Комментарии находятся в metadata блоков ipynb, код/текст — в cells, результаты —
в outputs. Эти вложенные структуры файла не являются таблицами PostgreSQL.
Их поля, ключи и права описаны в [relations.md](relations.md#s3-ipynb-файлы-окружения-и-аватары).
Redis Pub/Sub передаёт временные уведомления о комментариях по WebSocket;
не создаёт отдельную таблицу или постоянную копию комментариев.
