#!/usr/bin/env bash
set -euo pipefail

coverage_dir=$(mktemp -d)
trap 'rm -rf "$coverage_dir"' EXIT

# Прогоняем все пакеты, затем исключаем сгенерированный код и утилиты только из покрытия.
go test -race -count=1 -coverpkg=./... -coverprofile="$coverage_dir/raw.out" ./...
# cmd/main только запускает приложение; internal/app остаётся в отчёте.
awk 'NR == 1 || $1 !~ /\/(internal\/transport\/http\/api|cmd\/main|cmd\/apidog|migrations)\//' \
  "$coverage_dir/raw.out" > coverage.out

go tool cover -func=coverage.out > "$coverage_dir/summary.txt"
summary=$(awk '$1 == "total:" { print "Итоговое покрытие: " $3 }' "$coverage_dir/summary.txt")

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf '### Покрытие тестами\n\n%s\n' "$summary" >> "$GITHUB_STEP_SUMMARY"
fi
printf '%s\n' "$summary"
