---
type: guide
title: "Блокноты: редакции, PostgreSQL и S3"
description: "Владение, проверка редакции по хешу, публикация неизменяемых файлов и сохранение полей ipynb."
tags: [notebooks, persistence, ipynb]
sources:
  - id: openwiki-source-517011d1027a07f39186a00d
    resource: repo://internal/notebook/adapters/ipynb/ipynb.go
  - id: openwiki-source-d10720638244b28e3a08518b
    resource: repo://internal/notebook/adapters/ipynb/rewrite.go
  - id: openwiki-source-c1af176bb103419425143738
    resource: repo://internal/notebook/adapters/postgres/notebook.go
  - id: openwiki-source-7f9be0e2b42665c2af6d9c69
    resource: repo://internal/notebook/adapters/s3/files.go
  - id: openwiki-source-b1d926fa00f4ede267bc3709
    resource: repo://internal/notebook/delivery/http/editing.go
  - id: openwiki-source-50052b2ca6d0ab325ed24dce
    resource: repo://internal/notebook/usecase/editing.go
  - id: openwiki-source-0be68ea43b41f70debccebfc
    resource: repo://internal/notebook/usecase/notebook.go
generated: { by: "codex", at: "2026-11-02T21:58:51.903Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-02T21:58:51.903Z
---

# Блокноты: редакции, PostgreSQL и S3

## Владение и слои

HTTP delivery получает owner ID из principal. PostgreSQL выбирает строку одновременно по ID и owner ID; чужой блокнот выглядит отсутствующим. Get и Content проверяют владельца до чтения S3; List читает только метаданные. Usecase работает через Metadata, Files и NotebookCodec, которые app связывает с PostgreSQL, S3 и ipynb.Codec. См. [авторизацию](auth-sessions.md) и [HTTP-контракт](../operations/startup-contract.md).

## Создание и редактирование

Create обрезает края имени, отвергает пустое имя, создаёт один code-блок и пишет ipynb по UUID-ключу до metadata.Create. Ошибка записи строки не удаляет уже загруженный объект.

Content возвращает блоки и SHA-256 исходных байтов файла. Delivery выдаёт этот hash также как ETag; изменения принимают один непустой сильный If-Match, отвергая wildcard, списки и слабые теги. Несовпадение редакции отображается в 409. AddCell проверяет тип, лимит и индекс, EditCell/MoveCell/RemoveCell находят блок по ID. CreateCell и DeleteCell тоже проходят через общую проверку редакции.

changeCells выполняется внутри Metadata.Modify под SELECT FOR UPDATE строки владельца: читает текущий файл, проверяет hash, применяет изменение и вызывает Rewrite. Если блоки не изменились, сохраняются hash, file_key и updated_at. Иначе новый ipynb записывается под новым UUID-ключом; лишь затем SQL UPDATE меняет file_key, cells_count и updated_at и транзакция фиксируется.

Старый объект не изменяется и не удаляется: читатель со старой ссылкой продолжает получать согласованный снимок. Общей транзакции S3/PostgreSQL нет. При откате SQL новая загрузка остаётся без ссылки; при неопределённом результате commit код возвращает ошибку и не удаляет потенциально опубликованный объект. Сам S3 Put заменяет объект при повторном использовании ключа, но путь редактирования выбирает новый ключ.

## Формат и ограничения

Parse принимает code/markdown и source как строку либо массив строк. Build создаёт nbformat 4.5 с пустыми outputs и null execution_count у code-блоков; это создание нового документа. Rewrite сохраняет прочие поля документа и существующих блоков, включая metadata и результаты неизменённых блоков. При изменении source code-блока сбрасываются только его outputs и execution_count; пустые и повторяющиеся ID отвергаются. Это преобразование JSON, без обещания побайтового сохранения форматирования.

S3 использует AWS default config и path-style, Get/Put имеют timeout пять секунд. Get проверяет ContentLength и ограничивает фактически прочитанное тело; Put и usecase также проверяют MaxNotebookBytes. Значения лимитов находятся в `internal/notebook/usecase/contract.go`; значения подключения — в конфигурации. Наличие ipynb-кодека не доказывает исполнение блоков или работу прода.

## Проверки

`internal/notebook/usecase/editing_test.go` покрывает конфликт конкурентных писателей, отсутствие изменений, старый снимок и ошибку публикации. `adapters/postgres/notebook_test.go` проверяет commit и отсутствие UPDATE при no-op; `adapters/ipynb/rewrite_test.go` — сохранение полей и сброс outputs только изменённого блока. Input-security тесты проверяют хранение текстовых данных без HTML-экранирования; безопасный вывод остаётся обязанностью клиента.

[Карта Go API](../quickstart.md)

## Источники
- [internal/notebook/usecase/notebook.go#L29-L95](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/usecase/notebook.go#L29-L95)
- [internal/notebook/usecase/editing.go#L13-L170](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/usecase/editing.go#L13-L170)
- [internal/notebook/adapters/postgres/notebook.go#L25-L111](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/adapters/postgres/notebook.go#L25-L111)
- [internal/notebook/delivery/http/editing.go#L14-L88](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/delivery/http/editing.go#L14-L88)
- [internal/notebook/adapters/ipynb/ipynb.go#L33-L93](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/adapters/ipynb/ipynb.go#L33-L93)
- [internal/notebook/adapters/ipynb/rewrite.go#L11-L73](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/adapters/ipynb/rewrite.go#L11-L73)
- [internal/notebook/adapters/s3/files.go#L20-L98](https://github.com/Cringe-Driven-Development-Team/backend/blob/978bc2df36a71c796b95b14315f8e4d9b17a17b9/internal/notebook/adapters/s3/files.go#L20-L98)
