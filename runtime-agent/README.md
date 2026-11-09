# Runtime agent

Одна VPS обслуживает одну назначенную пользовательскую сессию. Агент запускает
один Docker-контейнер с одним kernel Python или R. Язык и UUID сессии передаются
при запуске сервиса; VPS создаёт и удаляет backend (`internal/runtime`).
Клиент API — доверенный backend, проверяющий права пользователей.
Агент не создаёт VPS, не обращается к S3/PostgreSQL и не изменяет ipynb.

## Разработка и проверки

Нужны Linux, Python 3.13 и Docker Engine с доступом служебного пользователя к socket.
Запуск агента от root запрещён. Docker Desktop должен поддерживать доступ хоста
к IPv4 контейнера; основной целевой стенд — Linux Docker Engine.

```bash
make agent-setup agent-lint agent-test agent-build
make agent-images agent-integration
make agent-test-jupyter  # настоящий Python kernel на локальном TCP без Docker
```

Зависимости устанавливаются только в `runtime-agent/.venv`.
`AGENT_PYTHON` и `AGENT_VENV` переопределяют интерпретатор и virtualenv.
Docker-тесты создают временную bridge-сеть для проверки протокола, изоляции
файлов, лимитов, OOM и восстановления. Эта сеть не является production firewall.
Сетевая приёмка выполняется отдельно: [контракт infra](../docs/runtime-agent-network.md).

## Назначение и запуск

VPS создаёт и настраивает модуль `internal/runtime` backend
([docs/runtime-provisioning.md](../docs/runtime-provisioning.md)): воркер запускает
playbook infra (роль `runtime_host` готовит образы, bridge-сеть, DNS и firewall).
Перед стартом сервиса он задаёт `SESSION_ID`, `KERNEL_LANGUAGE`, `KERNEL_NETWORK`, `KERNEL_DNS` и свежий
`RUNTIME_TOKEN` в защищённом env-файле, затем запускает сервис. Без корректной
конфигурации и существующей IPv4 bridge-сети агент не стартует; fallback нет.
Язык — `python|r`, UUID канонизируется. Наличие сети не доказывает наличие firewall.

Один worker обязателен. SQLite и эксклюзивная блокировка находятся в `STATE_DIR`.
Другой UUID или язык при том же каталоге состояния отклоняются. Первый запуск
создаёт kernel; повторный запуск агента очищает его прежние контейнеры, помечает
незавершённые команды `UNKNOWN`, сессию — `STOPPED`. Kernel автоматически не
восстанавливается и код не повторяется. Для новой сессии backend создаёт новую
VPS (`internal/runtime`), старую удаляет; журнал перед этим забирает backend.

## API

Все HTTP-маршруты, включая `/health` и `/openapi.json`, и WS handshake требуют
`Authorization: Bearer <RUNTIME_TOKEN>`. По умолчанию агент слушает `127.0.0.1:8000`.
Production-доступ из Go API, защищённый транспорт и firewall настраивает infra.

| Метод | Путь | Назначение |
| --- | --- | --- |
| GET | `/health` | UUID сессии, язык, состояние, ready/draining, generation, активная команда, очередь |
| POST | `/executions` | `{execution_id, code, timeout_seconds?}` → 202 |
| GET | `/executions/{execution_id}` | Состояние и результат исполнения |
| POST | `/variables` | `{execution_id}` → 202; инспекция в общей очереди |
| POST | `/kernel/interrupt` | Прервать активную команду |
| POST | `/kernel/restart` | Перезапустить свободный kernel, сохранив workspace |
| POST | `/runtime/activity` | Продлить активность сессии |
| POST | `/runtime/drain` | Запретить новые команды, отменить очередь, прервать активную |
| POST | `/runtime/stop` | Остановить контейнер; журнал остаётся доступен |
| GET | `/events?after=0` | Следующая страница, до 100 событий |
| WS | `/events/ws?after=0` | Replay и последующие события |

```bash
curl --fail-with-body http://127.0.0.1:8000/executions \
  -H "Authorization: Bearer $RUNTIME_TOKEN" -H 'Content-Type: application/json' \
  -d '{"execution_id":"run-1","code":"x = 42\nprint(x)\nx * 2"}'
curl --fail-with-body http://127.0.0.1:8000/executions/run-1 \
  -H "Authorization: Bearer $RUNTIME_TOKEN"
```

`execution_id` — 1–128 символов; для URL используйте percent-encoding.
Код — до 1 048 576 символов, HTTP body — до 2 MiB. Timeout — 1–86400 секунд,
по умолчанию 300; инспекция — 5 секунд. Очередь — 100 ожидающих команд.
Переполнение даёт `429`, недоступный/остановленный runtime — `503`.
`/health` отвечает 503 после drain, stop, потери kernel или Docker.

Backend сохраняет execution_id до отправки. Повтор с теми же параметрами
возвращает сохранённое состояние, включая после stop; другое содержимое — `409`.
Состояния команд: `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `INTERRUPTED`,
`TIMED_OUT`, `CANCELLED`, `UNKNOWN`. UNKNOWN нельзя автоматически повторять.
После stop, истечения простоя или сбоя новую сессию этот агент не принимает.

## Вывод и события

События содержат `seq`, `session_id`, `execution_id`, `kind`, `timestamp`.
`kernel_message` включает channel, msg_type, content, metadata, parent_msg_id,
generation и при наличии buffers_base64. Поздний вывод относится к исходной
команде; неизвестный parent — к unattributed output.

`MAX_OUTPUT_BYTES` ограничивает суммарные UTF-8 JSON-данные kernel на команду:
все типы сообщений, metadata, buffers, reply и нормализованный результат инспекции.
Копии результата в таблице executions и событии состояния учитываются обе.
Конверты событий и фиксированные служебные сводки не входят в этот бюджет;
код принятой команды хранится отдельно и ограничен HTTP-валидацией.
При превышении один раз появляется `output_truncated`, новые данные отбрасываются,
а результат при необходимости заменяется `{status, output_truncated:true}`.
Это не меняет состояние исполнения и не мешает обработать reply + idle.
Для unattributed output действует отдельный бюджет на поколение kernel.
Входящий кадр — до 32 MiB; stdout инспекции R в памяти — до 1 MiB.

Backend фиксирует события и свой курсор одной транзакцией, дедуплицируя по
`(session_id, seq)`. WS heartbeat через 15 секунд не имеет seq; медленный клиент
отключается после 10 секунд ожидания отправки. Переподключение использует последний
сохранённый курсор. Несколько подписчиков получают независимые уведомления.

Инспекция возвращает до 200 переменных `{name,type,preview}`, preview до 200 символов
для простых значений. Инспекция не добавляется в историю; её служебный вывод
не выдаётся как вывод ячейки. Её итоговый результат также ограничен бюджетом.

## Жизненный цикл и поставка

Простой 15 минут завершает контейнер. Команды, управление и `/runtime/activity`
обновляют активность; чтение результатов и heartbeat — нет. Активное исполнение
и очередь защищены от idle timeout. Timeout/interrupt ждёт подтверждения до 5 секунд;
без подтверждения команда становится UNKNOWN, контейнер останавливается.
Потеря kernel/канала завершает сессию. Ошибка удаления контейнера оставляет
сессию DRAINING/cleanup_failed до успешного повтора monitor или recovery.

Контейнер работает с UID/GID 65532, без host mounts и опубликованных портов,
с read-only rootfs, 1 CPU / 1 GiB RAM / 128 процессов, временными workspace
256 MiB и /tmp 64 MiB. Пакеты из `%pip install` ставятся в отдельный tmpfs
/packages 512 MiB (`PYTHONUSERBASE`), импортируются без перезапуска kernel и
удаляются вместе с сессией; память tmpfs входит в лимит 1 GiB. Изменение прав внутри контейнера не затрагивает файлы хоста.
Агент обращается к Jupyter по закрытому TCP 8888 с отдельным токеном контейнера.
Kernel и Jupyter находятся в одной пользовательской границе доверия; отдельный
токен Jupyter не является секретом от собственного kernel. Токен агента и SQLite
в контейнер не передаются. Уничтожить свой kernel пользователь может.

`deploy/cellestial-agent.service` и `deploy/agent.env.example` — шаблоны для infra,
не production-выкатка. Настройки: HOST, PORT, RUNTIME_TOKEN, STATE_DIR, SESSION_ID,
KERNEL_LANGUAGE, KERNEL_NETWORK, KERNEL_DNS, IDLE_TIMEOUT_SECONDS, MAX_OUTPUT_BYTES,
PYTHON_IMAGE, R_IMAGE. Автоматического удаления истории нет.

При Docker Desktop WSL обычно не имеет маршрута к bridge IP контейнера; native
`agent-integration` тогда завершится timeout. Тесты нужно запускать на Linux Engine
или из доверенного runner в host network namespace Docker Engine. Это ограничение
стенда не исправляется публикацией Jupyter-порта. Production-проверки требуют VPS.

Пример доверенного runner (только разработка; Docker socket даёт ему полномочия хоста).
В отличие от пользовательского kernel ему нужны socket и read-only исходники:

```bash
docker run --rm --network host --user "$(id -u):$(id -g)" \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --mount "type=bind,source=$PWD/runtime-agent,target=/repo,readonly" \
  --mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock \
  --entrypoint sh cellestial-kernel-python:local -c '
    mkdir /tmp/agent
    tar -C /repo --exclude=.venv --exclude=.serena --exclude=dist --exclude=build \
      --exclude=__pycache__ --exclude=.pytest_cache --exclude=.ruff_cache -cf - . \
      | tar -C /tmp/agent -xf -
    python -m venv --system-site-packages /tmp/testenv
    cd /tmp/agent
    /tmp/testenv/bin/pip install --no-cache-dir -r requirements-dev.txt &&
      RUN_DOCKER_TESTS=1 /tmp/testenv/bin/python -m pytest -q -m docker
  '
```
