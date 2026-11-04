# Wiki backend

Создавай производную русскоязычную wiki Go API на точном исходном SHA.
Код в main, зелёный CI и наличие образа не доказывают поведение прода.
Не объявляй код проверенным на проде: для этого нужна отдельная проверка стенда.
Не меняй исходники, AGENTS.md или эти инструкции. Генерация запускается только
вручную владельцем, без расписаний и workflow с моделью.

## Источники

Корпус задают `.openwikiignore` и фильтр `scripts/openwiki-check.ts`:
`internal/**`, `cmd/**`, `migrations/**`, `build/**`, `scripts/**`,
`.github/workflows/**`, go.mod, go.sum, Makefile, docker-compose.yml,
docker-compose.override.yml, .golangci.yml, .editorconfig, .gitignore,
.dockerignore, README.md, AGENTS.md, .openwikiignore, этот brief и
`docs/{authentication,input-security,redis-sessions,local-development,release,apidog}.md`.
Не читай планы, runtime-agent, env-файлы, ключи, зависимости, mocks и outputs.
`internal/transport/http/api/{api.gen.go,openapi.yaml}` исключены: generated API
не является самостоятельным доказательством; владелец HTTP-контракта — Apidog.
Сгенерированные `openwiki/**` доступны lifecycle, но не служат первоисточником.

В quickstart.md дай карту Go API: cmd/main → internal/app, config,
transport/http, user/notebook → delivery/usecase/domain/adapters, миграции,
тесты и команды разработки. Объясняй только подтверждённое кодом поведение:
auth, Redis-сессии, границу BFF/S2S, middleware, хранение блокнотов и ipynb.
Отделяй HTTP-контракт Apidog от реализации обработчиков. Не выводи наличие
интеграции с runtime-agent из планов: самостоятельный Python-сервис вне корпуса.
Общепроектную карту ищут в docs, клиент/BFF — во frontend, выкладку — в infra.
Будущие фичи ищут в модулях docs, не в wiki.

Каждую тематическую страницу подкрепляй Claims и ссылками на реальные строки:
https://github.com/Cringe-Driven-Development-Team/backend/blob/<исходный gitHead>/<путь>#L<строка>.
Объясни в quickstart: repo:// обозначает файл backend на gitHead из
.last-update.json; wiki и Claims производны, перед правкой сверяют актуальный код.
Не публикуй секреты, IP/CIDR и внутренние адреса; вместо значений указывай путь
к конфигурации. Пиши заголовки и пояснения по-русски, сохраняя имена API и команд.
Связывай страницы относительными Markdown-ссылками и relatedPages.

В quickstart объясни порядок чтения: `openwiki_search` → `openwiki_read` с ID wiki
из результата; если ответа достаточно и нет противоречий, поиск завершён.
При обычном чтении опубликованная wiki считается актуальной; её актуальность
обеспечивает процесс обновления и публикации. Читатель не проверяет свежесть,
не сравнивает SHA и не перечитывает исходники ради перепроверки ответа.
К конкретному исходнику переходят при пробеле, противоречии или для правки;
явные ошибки инструментов не игнорируют. Без MCP читают quickstart и нужную
Markdown-страницу с тем же правилом остановки; без wiki — документацию и исходники.
Для простой правки известного файла поиск по wiki не обязателен. Wiki не является
мониторингом: состояние прода прямо сейчас требует соответствующего свидетельства.
Эти правила чтения не отменяют проверки источников и Claims при генерации.

Используй штатный OpenWiki init/update с language: "ru", заверши openwiki_finish.
Сгенерированные страницы, Claims и checkpoint идут отдельным wiki-only PR;
`INSTRUCTIONS.md` меняют только в исходном PR. CI проверяет freshness к базе PR
и запрещает любые исходные изменения в wiki PR. Проверки свежести относятся
к обслуживанию, генерации, CI и явной диагностике, а не обычному чтению.
