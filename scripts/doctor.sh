#!/usr/bin/env bash
# Read-only preflight. Never source .env or print credentials.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
failures=0
ok() { printf 'OK   %s\n' "$*"; }
warn() { printf 'WARN %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
need() {
    if command -v "$1" >/dev/null 2>&1; then ok "$1"; else fail "$1 — $2"; fi
}

need git 'установите Git'
need make 'установите Make'
need curl 'установите curl'
need go 'установите Go версии из go.mod'
need gopls 'go install golang.org/x/tools/gopls@v0.20.0; добавьте каталог go env GOPATH/bin в PATH'
need docker 'установите Docker Engine и Compose v2'
need gh 'установите GitHub CLI и выполните gh auth login'
need npm 'установите Node.js >= 22.12 с npm: Redocly для make generate и make check'

if command -v go >/dev/null 2>&1; then
    # Inspect the executable in PATH without Go auto-downloading a toolchain.
    export GOTOOLCHAIN=local
    required=$(awk '$1 == "go" {print $2}' go.mod)
    installed=$(go env GOVERSION 2>/dev/null) || installed=''
    if awk -v have="${installed#go}" -v want="$required" 'BEGIN {
        split(have,h,"."); split(want,w,".");
        for (i=1;i<=3;i++) {if (h[i]+0>w[i]+0) exit 0; if (h[i]+0<w[i]+0) exit 1}
        exit (have == "")
    }'; then ok "Go $installed (go.mod: $required)"; else fail "Go ${installed:-не определён} — нужна версия >= $required в PATH; установите её или добавьте уже установленный toolchain в PATH (автозагрузка в doctor отключена)"; fi
    if [ "$(go env CGO_ENABLED 2>/dev/null)" = 1 ]; then ok 'CGO_ENABLED=1 (-race)'; else fail 'для make test нужен CGO_ENABLED=1'; fi
    compiler=$(go env CC 2>/dev/null) || compiler=cc
    need "${compiler%% *}" 'установите C-компилятор для go test -race (например, build-essential)'
    module=$(go env GOMOD 2>/dev/null) || module=''
    if [ "$module" = "$PWD/go.mod" ]; then ok 'Go workspace: корень backend'; else fail 'workspace — запустите из backend; проверьте GOWORK/GO111MODULE'; fi
fi
if command -v gopls >/dev/null 2>&1; then
    version=$(gopls version 2>/dev/null) || version=''
    if [[ "$version" == *'v0.20.0'* ]]; then ok 'gopls v0.20.0'; else warn 'проверенная версия gopls — v0.20.0; проверьте совместимость'; fi
    if gopls mcp -help >/dev/null 2>&1; then ok 'gopls mcp'; else fail 'gopls без MCP — установите v0.20.0'; fi
fi

if command -v docker >/dev/null 2>&1; then
    if docker info >/dev/null 2>&1; then ok 'Docker daemon'; else fail 'Docker daemon недоступен — запустите Docker и проверьте права/песочницу'; fi
    if docker compose version >/dev/null 2>&1; then
        ok 'Docker Compose v2'
        if docker compose config --quiet >/dev/null 2>&1; then
            ok 'Compose configuration'
            config_env=$(docker compose config --environment 2>/dev/null) || config_env=''
            jwt=$(printf '%s\n' "$config_env" | awk '/^JWT_SECRET=/{sub(/^[^=]*=/,""); print}')
            bff_key=$(printf '%s\n' "$config_env" | awk '/^BFF_API_KEY=/{sub(/^[^=]*=/,""); print}')
            for secret in JWT_SECRET BFF_API_KEY; do
                if [ "$secret" = JWT_SECRET ]; then value=$jwt; else value=$bff_key; fi
                if [[ "$value" =~ [^[:space:]] ]]; then ok "$secret задан"; else fail "$secret пуст — скопируйте .env.example в .env и задайте отдельный случайный секрет (docs/local-development.md)"; fi
            done
            unset config_env jwt bff_key value
        else fail 'Compose configuration — проверьте .env и выполните docker compose config --quiet'; fi
    else fail 'установите Docker Compose v2 с поддержкой up --wait'; fi
fi
if command -v gh >/dev/null 2>&1; then
    if gh auth status >/dev/null 2>&1; then
        ok 'gh authentication'
        for repo in Cringe-Driven-Development-Team/backend; do
            if gh api "repos/$repo" --jq .permissions.push 2>/dev/null | awk '$0 == "true" {ok=1} END {exit !ok}'; then
                ok "GitHub write access: $repo"
            else fail "нет доступа к $repo — проверьте gh auth status, членство, сеть и разрешения песочницы"; fi
        done
    else fail 'gh — выполните gh auth login; проверьте сеть и разрешения песочницы'; fi
fi
client=0
for cli in codex claude; do
    if command -v "$cli" >/dev/null 2>&1; then ok "$cli найден; MCP trust и подключение проверьте в новой сессии по docs/local-development.md"; client=1; fi
done
if [ "$client" = 0 ]; then fail 'установите Codex или Claude Code для работы агента'; fi
warn 'cdd-tasks, MCP trust и Projects проверяются по docs/local-development.md; doctor не подтверждает их'
if [ ! -x bin/golangci-lint ]; then warn 'golangci-lint ещё не установлен; make lint скачает версию из Makefile'; fi
printf '\nОшибок: %s\n' "$failures"
[ "$failures" -eq 0 ]
