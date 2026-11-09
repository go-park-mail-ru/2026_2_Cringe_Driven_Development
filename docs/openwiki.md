# OpenWiki backend

[Wiki кода](../openwiki/quickstart.md) описывает Go API на исходном SHA.
Для поиска контекста: `openwiki_search` → `openwiki_read` с ID wiki из результата.
При обычном чтении опубликованная wiki считается актуальной: если ответа достаточно
и нет противоречий, остановитесь без проверки свежести, сравнения SHA и перепроверки
по исходникам. Конкретный исходник нужен при пробеле, противоречии или для правки;
явные ошибки инструментов не игнорируйте. Для простой правки известного файла поиск не обязателен.
Актуальность обеспечивает процесс обновления и публикации. Для обслуживания и явной
диагностики: `bun scripts/openwiki-check.ts --against <полный актуальный SHA backend/main>` (нужен Bun).
Если wiki ещё не создана, используйте [локальную документацию](local-development.md#код-и-документация) и исходники.
[Brief генерации](../openwiki/INSTRUCTIONS.md) задаёт корпус и границы; общий поиск четырёх репозиториев
настраивается по [README docs](https://github.com/Cringe-Driven-Development-Team/docs#readme).
Без MCP читайте quickstart и нужную Markdown-страницу с тем же правилом остановки.
Код и wiki не доказывают состояние прода прямо сейчас: нужны соответствующие свидетельства.

Владелец вручную обновляет wiki из соседнего клона docs: `bun run wiki:update-local --repo ../backend`
(после первоначального OpenWiki init). Путь `--repo` считается от текущего каталога;
для worktree укажите его фактический путь. Страницы и checkpoint идут отдельным wiki-only PR;
CI публикует уже обновлённую wiki и не запускает модель.

Изменения `openwiki/**` в `main` запускают [Publish docs wiki](../.github/workflows/publish-docs-wiki.yml):
он вызывает `pages.yml` в `docs/main`; для диагностики доступен ручной запуск.
Нужен secret `DOCS_PAGES_TOKEN` с Actions: write только для репозитория docs;
настройка и публичная wiki — в [README docs](https://github.com/Cringe-Driven-Development-Team/docs#readme).
Локальная проверка dispatch без GitHub: `bun test scripts/openwiki-publish.test.ts`.

