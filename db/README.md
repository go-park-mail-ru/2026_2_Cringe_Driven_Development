# ДЗ №2 - внедрение СУБД

PostgreSQL 18, схема `colab`, десять таблиц. Драйвер - **pgx/v5**, миграции -
**Goose v3.28.0**. DDL и запросы написаны вручную на SQL.

[Отношения и ограничения](normalized/relations.md) · [ER-диаграмма](normalized/diagrams.md) ·
[Миграции](migrations/) · [Запросы](queries/)

## Запуск

Нужны Docker, `psql` и Goose v3.28.0.
Команды выполняются из корня репозитория. БД - UTF8; роль должна иметь право
создавать схему и расширение `btree_gist`.

```bash
docker run --detach --name colab-db-hw2 \
  -e POSTGRES_USER=colab -e POSTGRES_PASSWORD=colab -e POSTGRES_DB=colab_hw2 \
  -p 127.0.0.1:55432:5432 postgres:18-alpine
export DB_DSN='postgres://colab:colab@localhost:55432/colab_hw2?sslmode=disable'
docker exec colab-db-hw2 pg_isready -U colab -d colab_hw2
```

После готовности PostgreSQL:

```bash
goose -dir db/migrations postgres "$DB_DSN" up
goose -dir db/migrations postgres "$DB_DSN" status
goose -dir db/migrations postgres "$DB_DSN" down
goose -dir db/migrations postgres "$DB_DSN" up
```

`up` применяет новые миграции, `down` откатывает одну, `down-to 0` удаляет всю
схему `colab` с данными. Расширение `btree_gist` сохраняется. Миграции транзакционны:
схема → триггеры → начальные данные → защита оплаченных тарифов и версий строк.
В начальных данных: Alice/Bob с паролем `DemoPass123!`, Free/Pro/Max и промокод `DEMO2026`.

## Запросы и транзакции

[all.sql](queries/all.sql) загружает `PREPARE` в текущее соединение `psql`:

```bash
psql -X -v ON_ERROR_STOP=1 "$DB_DSN" -f db/queries/all.sql -c "EXECUTE find_credentials('alice');"
```

Параметры и результаты описаны в комментариях запросов. Повторная загрузка
в том же соединении требует `DEALLOCATE ALL`.

| Файл | Операции |
|---|---|
| [users.sql](queries/users.sql) | Регистрация, учётные данные и профиль |
| [projects.sql](queries/projects.sql) | Проекты, проверка доступа, переименование и шеринг |
| [environment.sql](queries/environment.sql) | Пакет файлов окружения, изменение пути и удаление связи |
| [subscriptions.sql](queries/subscriptions.sql) | Блокировки, платёж и таймлайн подписки |
| [runtime.sql](queries/runtime.sql) | Квота, старт, завершение и история исполнений |

Основные правила:

- **READ COMMITTED**: сначала строка пользователя `FOR UPDATE`, затем тариф
  `FOR SHARE`. Блокировка и последующее чтение - отдельные запросы.
- [Покупка](queries/purchase.sql): после блокировок фиксируется время T; платёж,
  новый интервал и непустые остатки прежних интервалов записываются атомарно.
  Первоначальные даты окончания сохраняются; перед `COMMIT` проверяются отложенные
  ограничения. Промокод полностью покрывает оплату и используется пользователем один раз.
- [Старт](queries/start.sql): под блокировкой пользователя проверяется квота.
  Расход - сумма секунд исполнения за сутки UTC, включая параллельные и активные
  запуски, без округления. Смена тарифа не сбрасывает расход. Worker контролирует
  дальнейшее исчерпание квоты; сетевые вызовы и ожидание worker выполняются вне транзакции.
- Оптимистическое обновление проверяет прежний `updated_at` и возвращает новый.
  Версия строго возрастает; сохраняется микросекундная точность. Ноль строк -
  конфликт либо отсутствие доступа/записи. Повторное завершение запуска не меняет его конец.
- Создание проекта с файлом и пакет файлов окружения атомарны. `file_path` - ключ
  S3; загрузка и уборка объектов выполняются отдельно от SQL-транзакции.
- В pgx тело запроса хранится строковой константой, значения передаются аргументами
  `$1…$n`. Все шаги идут через один `pgx.Tx` с `r.Context()`; ошибки требуют `Rollback`,
  результат `Commit` проверяется. После ошибки в интерактивном `psql` нужен `ROLLBACK`.

При `40001` или `40P01` повторяется вся транзакция, максимум три попытки с задержкой.
Нарушения ограничений разбираются по SQLSTATE и имени ограничения. При неизвестном
исходе `COMMIT` платёж автоматически не повторяется.

## Соответствие требованиям

| Требование | Решение |
|---|---|
| 1–2. Драйвер, без ORM | pgx/v5; SQL написан вручную |
| 3. SQL-миграции | Goose Up/Down в `migrations/` |
| 4. Постоянные запросы | Параметризованные шаблоны; строковые константы для pgx |
| 5. Рекомендации PostgreSQL | IDENTITY, text, timestamptz, numeric; EXISTS/NOT EXISTS, полуоткрытые интервалы |
| 6. Согласованность | Транзакции, пакетные вставки, блокировки, EXCLUDE и проверка версии |
| 7. Без преждевременной оптимизации | Нормализация сохранена; новые индексы для ускорения не добавлены |

Источники: [ограничения](https://www.postgresql.org/docs/18/ddl-constraints.html),
[изоляция](https://www.postgresql.org/docs/18/transaction-iso.html),
[Don't Do This](https://wiki.postgresql.org/wiki/Don't_Do_This),
[Goose](https://pressly.github.io/goose/documentation/cli-commands/).
