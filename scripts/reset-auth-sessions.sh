#!/bin/sh
# Запускать только при остановленных инстансах API, до возобновления auth-трафика.
set -eu
if [ "${AUTH_API_STOPPED:-}" != yes ]; then
  echo 'Stop every API instance and set AUTH_API_STOPPED=yes.' >&2
  exit 1
fi
namespace=${AUTH_REDIS_NAMESPACE:-}
case "$namespace" in
  *[!a-zA-Z0-9:_-]*) echo 'Invalid AUTH_REDIS_NAMESPACE.' >&2; exit 1 ;;
esac
if [ -n "${REDIS_PASSWORD:-}" ]; then
  export REDISCLI_AUTH=$REDIS_PASSWORD
else
  unset REDISCLI_AUTH
fi
redis_cli() {
  redis-cli -e --no-auth-warning -h "${REDIS_HOST:-redis}" \
    -p "${REDIS_PORT:-6379}" -n "${REDIS_DB:-0}" "$@"
}
# SCAN может вернуть дубликаты; DEL идемпотентен. Во время сброса писатели запрещены.
for family in auth_session refresh_session user_session_version; do
  redis_cli --scan --pattern "${namespace}${family}:*" |
    while IFS= read -r key; do
      redis_cli DEL "$key" > /dev/null
    done
  # Отдельная проверка, она же ловит упавший SCAN в пайплайне выше.
  remaining=$(redis_cli --scan --pattern "${namespace}${family}:*")
  if [ -n "$remaining" ]; then
    echo 'Authorization reset incomplete; keep the API stopped.' >&2
    exit 1
  fi
done
echo 'Authorization namespace cleared; all users must log in again.'
