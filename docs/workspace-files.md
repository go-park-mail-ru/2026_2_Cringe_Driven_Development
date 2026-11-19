# Файлы временного workspace

Владелец готовой runtime-сессии работает с временным дисковым `/workspace`.
Restart kernel сохраняет файлы и workspace generation. Stop и idle cleanup закрывают
подписки и передачи, удаляют пользовательское дерево и staging. Перезапуск процесса
агента сохраняет файловые checkpoint и запускает kernel с прежними файлами.
Незавершённые исполнения при таком восстановлении получают UNKNOWN.

## Подготовка хоста

Infra #161 создаёт ext4 размером 5 ГиБ на системном диске VPS и проверяет запас
не менее 4 ГиБ на системной ФС. Размер ext4 включает её метаданные. Агент получает
`WORKSPACE_PATH=/srv/cellestial/workspace`. Корень принадлежит root. Внутри находятся
`data` с владельцем runtime-agent, группой 65532 и режимом 2770, а также `staging`
с владельцем runtime-agent и режимом 0700. Процессу агента требуется основная
или дополнительная группа 65532 для проверки каталога и публикации загрузок.
В текущей роли из infra PR #165 для учётной записи указана только дополнительная
группа docker.
Kernel наследует umask 0002, загруженные файлы при commit получают группу data. Kernel с UID/GID 65532 видит только
writable bind mount `data` как `/workspace`. Rootfs остаётся read-only.

Оба дерева используют одну файловую систему. Поэтому Python и незавершённые загрузки
исчерпывают общую жёсткую квоту. Агент проверяет mount, принадлежность каталогов той же
ФС, доступ и приватность staging. Systemd preflight infra проверяет ext4 backing file.
Для локального запуска без WORKSPACE_PATH сохраняется tmpfs, файловый API недоступен.

Настройки подготовки хоста и прокси описаны в задаче infra #161.

## Внешний контракт версии 1

Далее описано ожидаемое поведение BFF и браузерного клиента по контракту №118.
Реализованные точки входа Go и агента приведены в разделе о внутреннем транспорте.

BFF принимает управляющий WSS по `/api/runtime-sessions/{id}/files/ws`, отдельный
upload WSS по `/api/runtime-sessions/{id}/files/uploads/{transfer_id}/ws` и GET
скачивания по `/api/runtime-sessions/{id}/files/downloads/{transfer_id}`.
Все адреса same-origin. Handshake и download описаны в `spec/openapi.json`.
Команды описаны в `spec/workspace/v1/commands.schema.json`, примеры лежат рядом.

WSS проверяет текущую HttpOnly cookie, точный Origin APP_ORIGIN и Sec-Fetch-Site
same-origin при его наличии. После refresh BFF отправляет session_cookie_required с session_sync_url. Клиент
выполняет по этому адресу POST с X-CSRF: 1, чтобы получить обновлённую HttpOnly
cookie через Set-Cookie в течение 30 секунд. Предыдущая cookie также принимается
только в течение 30 секунд перехода. Origin и привязка cookie к socket проверяются повторно.
Этот обработчик нужен и при активном нативном скачивании.

Нативный GET принимает cookie и проверяет
Sec-Fetch-Site. Знание transfer UUID не заменяет авторизацию. На каждой команде,
части и открытии скачивания Go проверяет владельца и состояние READY, агент
проверяет назначенную сессию и готовность kernel. BFF каждые 15 секунд проверяет
текущую сессию и runtime. Heartbeat закрывает socket после 60 секунд без pong.

Envelope содержит `protocol_version`, `type`, `request_id`, `session_id` и
`workspace_generation`. Терминальное workspace_closed во внешнем WSS использует зарезервированный
seq 9007199254740991. После него socket закрывается, продолжение replay этой
generation запрещено. BFF выдаёт его также при HTTP 410 от Go, если процесс
агента уже недоступен после stop.

Generation файлов равна UUID runtime-сессии и отличается
от поколения kernel. Новая runtime-сессия всегда получает другой UUID.
Для мутаций и создания transfers требуется отдельный UUID `operation_id`.
Ответ содержит `type=response`, тот же request_id и `result` либо `error`.
Неизвестные поля и параметры другой команды отклоняются.

Поддерживаются list, subscribe, unsubscribe, mkdir, rename, delete, upload_begin,
upload_status, upload_commit, download_begin, transfer_status и transfer_cancel.
Control socket принимает JSON до 64 КиБ. Серверные страницы ограничены по байтам.
Ответы команд идут последовательно, между ними могут приходить события.
Ошибка JSON/framing закрывает WSS с 1002, нарушение доступа с 1008, превышение
размера с 1009, внутренний сбой с 1011, переполнение буфера consumer с 1013.
Последний случай требует reconnect и проверки доступности replay.

Лимиты BFF составляют четыре управляющих socket на runtime, 256 на процесс и
64 upload data-channel на процесс. На агенте допускаются две активные передачи,
64 подписанных каталога на socket и 256 уникальных каталогов на runtime.
Передача части использует не более 1 МиБ payload. Credit window равен 4 МиБ.
Клиент дополнительно ограничивает WebSocket.bufferedAmount. Очередь управляющего
socket ограничена 256 КиБ, сумма входящих очередей BFF ограничена 64 МиБ.

## Дерево и события

Пути относительно data, корень обозначается пустой строкой. Абсолютные пути,
пустые компоненты, `.` и `..`, NUL, невалидный UTF-8, компоненты больше 255 байт
и пути больше 4095 байт отклоняются. Linux openat2 использует RESOLVE_BENEATH,
RESOLVE_NO_SYMLINKS и RESOLVE_NO_XDEV. Симлинки видны как entries с действиями
rename/delete самой ссылки. FIFO, socket и device видны как unsupported.

List читает непосредственных детей, default limit 100, диапазон 1–500. Папки идут
первыми, внутри группы сравниваются UTF-8 байты имени. Ответ может содержать меньше
limit entries для ограничения размера JSON. Курсор HMAC подписан токеном текущей
сессии и связан с generation, путём, revision и последним sort key. Revision
учитывает inode каталога и версии непосредственных детей. При изменении между
страницами возвращается resync_required. При удалении или замене самого каталога возвращается directory_invalidated,
клиент закрывает ветку и перечитывает родителя. Размер папок рекурсивно не вычисляется.

Subscribe сначала устанавливает inotify watches, затем возвращает revision каждого
каталога и seq перед snapshot. Данные страниц клиент запрашивает через list.
События после snapshot seq применяются поверх списка. Каждый list также возвращает
seq, снятый до его чтения. При гонке с записью допустимо повторное применение уже
отражённого entry, его version позволяет это распознать.

События содержат generation и монотонный seq. Created/updated несут entry,
removed несёт путь. Перемещения и неоднозначные изменения могут выражаться через
created/removed или directory_invalidated. Переименование самой наблюдаемой папки
инвалидирует её подписки и подписки потомков. Клиент перечитывает затронутую ветку.
Burst объединяется за 150 мс. Очередь изменившихся имён и журнал ограничены.

Журнал хранит до 4096 событий и 4 МиБ. Одна страница replay содержит до 256 событий
и 512 КиБ. Seq резервируется диапазонами на диске. После перезапуска процесса разрыв
диапазона явно требует resync. Потеря регистрации socket возвращает ошибку
resync_required и закрывает WSS с 1013, после reconnect клиент заново подписывается. Overflow inotify, потерянный cursor или переполнение
журнала также требуют resync. При недоступности watches агент раз в две секунды
инвалидирует только подписанные каталоги с synchronization=degraded.

Клиент отбрасывает события уже применённого seq, проверяет generation и сверяет
entry version. Revision является непрозрачным токеном равенства. Сравнивать строки
revision по величине нельзя. Ответ мутации подтверждается по operation_id и
resulting_seq, последующие события продолжают обновлять каталог. Один результат
может присутствовать в ответе команды и в событии watcher.

Mkdir создаёт один каталог. Rename работает в одном родителе и не перезаписывает
назначение. Rename/delete требуют version. Перед действием запись переносится
в приватный staging атомарным no-replace rename и проверяется по удерживаемому inode.
При подмене новый объект восстанавливается без перезаписи. Неоднозначный исход
возвращает operation_outcome_unknown и требует resync. Recursive delete допускается
только при recursive=true. Частичное удаление требует перечитывания и не означает
откат. В SQLite до действия записываются operation_id и fingerprint параметров.
Известный повтор возвращает сохранённый результат, другое тело даёт conflict.
Неизвестный исход после сбоя повторно не выполняется. История ограничена 4096
операциями за жизнь workspace, после достижения лимита новые операции получают
rate_limited. Завершение workspace удаляет файловые records.

## Upload и download

Upload_begin принимает полный относительный path, size_bytes и optional SHA-256.
Максимум равен 3221225472 байтам. Пустой файл допустим. Ответ содержит transfer_id,
chunk_bytes, credit_bytes, offset и generation. Binary frame содержит 16 байт UUID,
8 байт unsigned offset и 4 байта unsigned длины, затем payload. Все числа в network
byte order, заголовок занимает 28 байт. BFF последовательно передаёт bounded chunks
через Go агенту и возвращает upload_ack с подтверждённым offset.

ACK выдаётся после fdatasync staging и согласованного FULL SQLite checkpoint
offset/length/digest. Повтор подтверждённого chunk сверяется по SHA-256. Пропуск
offset и отличающиеся данные отклоняются. После сбоя хвост сверх checkpoint
обрезается, SHA-256 подтверждённой части восстанавливается потоковым чтением.
При возобновлении после reload пользователь выбирает файл повторно. Клиент проверяет
размер и подтверждённые chunks, повторно отправляя их и сравнивая ACK, затем продолжает
с серверного offset. После commit bytes и SHA-256 проверены, тот же inode переносится
в data через no-replace rename. Второй полной копии файла нет.

При ENOSPC/quota write обрывается с quota_exceeded. Начальная проверка места
не резервирует его у Python. Cancel удаляет staging и фиксирует очистку.
TTL неактивной передачи равен 30 минутам, очистка проверяется раз в две секунды.
Stop очищает staging и metadata. Закрытие control socket сохраняет upload для resume.

Download_begin возвращает transfer, размер, version и относительный download_url.
Нативный GET отдаёт application/octet-stream, attachment, Content-Length, ETag и
Accept-Ranges. Поддерживаются один Range и If-Range, offsets больше 2 ГиБ,
206 и 416. Несовпадающий If-Range приводит к полному ответу. Файл открывается
без перехода по ссылкам, regular file descriptor удерживается до завершения.
Изменение size/mtime/ctime/version прерывает поток с file_changed и событием ошибки.
Отмена браузерного запроса и transfer_cancel доходят до чтения агента.

Данные передаются порциями до 1 МиБ агентом и 64 КиБ Go relay. BFF отдаёт
ReadableStream напрямую браузеру. Ожидание consumer обеспечивает backpressure.
Read/write stall ограничен 60 секундами. Активность обновляется после переноса bytes
и пользовательских мутаций. Ping, подписки, list и автоматический resync её не обновляют.
Server-side completion подтверждает завершение HTTP-потока, browser отвечает за
сохранение на диск.

## Текущий внутренний транспорт и проверки

В текущем backend BFF обращается к `/api/v1/runtime-sessions/{id}/workspace/*`.
Go проверяет principal и владельца, затем вызывает агент по HTTPS/mTLS + Bearer.
Внутренние порты передают потоковые тела без раскрытия endpoint и credentials.
В `proto/cellestial/files/v1/files.proto` зафиксирован целевой gRPC-контракт.
Миграция внутренних adapters и генерация Go/TypeScript/Python принадлежат #123.

Request ID проходит BFF → Go → agent и SQLite instrumentation. Пути, содержимое
и credentials не попадают в метрики. Приватный `/workspace/metrics` агента отдаёт
Prometheus counters для bytes, ошибок, операций, длительности, очистки и gauges
socket/очереди событий. Его сбор согласуется с #119. Для долгого скачивания клиент сохраняет control
socket, чтобы получать progress и запросы синхронизации cookie.

Проверки выполняются через make check-fast, make check, make docker-build,
make agent-lint agent-test agent-build. Для отдельной
реальной проверки 3 ГиБ запустить из runtime-agent
`CELL_TEST_LARGE=1 .venv/bin/python -m pytest -q tests/test_workspace_large.py`.
Тест сверяет SHA-256 upload/commit/download и идентичность staging/data inode.
На стенде дополнительно проверить общую ext4 квоту 5 ГиБ с прямой записью Python,
несколько вкладок, медленного consumer, native download через browser/Caddy/BFF,
отзыв сессии, новый и прогретый runtime, kernel restart и stop cleanup.

Docker integration с дисковым workspace дополнительно включает
`tests/test_workspace_docker.py`. Он требует пустой одноразовый mount в
TEST_WORKSPACE_PATH и общую группу агента/kernel. При запуске агента внутри
Docker test runner TEST_WORKSPACE_VOLUME позволяет подключить kernel к data subpath
того же временного volume, сохраняя staging закрытым. Такая проверка подтверждает
права, запись из Python, чтение upload, сохранение после restart и stop cleanup.
Жёсткую квоту ext4 проверяют на disposable VPS, подготовленной infra #161.
