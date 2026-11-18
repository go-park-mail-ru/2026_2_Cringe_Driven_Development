#!/usr/bin/env bash
set -euo pipefail

coverage_dir=$(mktemp -d)
trap 'rm -rf "$coverage_dir"' EXIT

# Прогоняем все пакеты, затем исключаем сгенерированный код и утилиты только из покрытия.
go test -race -count=1 -coverpkg=./... -coverprofile="$coverage_dir/raw.out" ./...
# cmd/main только запускает приложение; internal/app остаётся в отчёте.
module=$(go list -m)
awk -v prefix="$module/" '
  NR == 1 { print; next }
  $1 ~ /\/(internal\/transport\/http\/api|cmd\/main|migrations)\// { next }
  {
    path = substr($1, length(prefix) + 1)
    sub(/:[0-9].*$/, "", path)
    if (!(path in generated)) {
      generated[path] = 0
      while ((getline line < path) > 0) {
        if (line ~ /^\/\/ Code generated .* DO NOT EDIT\.$/) {
          generated[path] = 1
          break
        }
        if (line ~ /^package /) break
      }
      close(path)
    }
    if (!generated[path]) print
  }
' "$coverage_dir/raw.out" > coverage.out

go tool cover -func=coverage.out > "$coverage_dir/summary.txt"
summary=$(awk '$1 == "total:" { print "Итоговое покрытие: " $3 }' "$coverage_dir/summary.txt")

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf '### Покрытие тестами\n\n%s\n' "$summary" >> "$GITHUB_STEP_SUMMARY"
fi
printf '%s\n' "$summary"

# Один блок может попасть в профиль из нескольких тестовых бинарников.
# Считаем его один раз и сравниваем покрытие до округления.
if ! awk '
  NR == 1 {
    if ($0 !~ /^mode: (set|count|atomic)$/) invalid = 1
    next
  }
  NF != 3 || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ { invalid = 1; next }
  $1 in statements && statements[$1] != $2 { invalid = 1; next }
  { statements[$1] = $2; if ($3 > 0) hit[$1] = 1 }
  END {
    for (block in statements) {
      total += statements[block]
      if (block in hit) covered += statements[block]
    }
  }
  END { exit (invalid || total == 0 || covered * 100 < total * 60) }
' coverage.out; then
  echo "Требуется корректный профиль с покрытием не менее 60%." >&2
  exit 1
fi
