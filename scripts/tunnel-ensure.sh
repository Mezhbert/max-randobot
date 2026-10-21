#!/usr/bin/env bash
# ============================================================================
# tunnel-ensure.sh — вахтёр туннеля для хостового cron (запуск раз в минуту).
# Если публичный URL из .env не отвечает 200 на /healthz — перезапускает
# туннель через dev-tunnel.sh (тот сам обновит WEBHOOK_URL и пересоздаст бота).
# Здоровый туннель — no-op за одну curl-проверку.
# ============================================================================
set -u
cd "$(dirname "$0")/.."
[ -f .env ] || exit 0
source <(grep -E '^(WEBHOOK_URL)=' .env)
[ -n "${WEBHOOK_URL:-}" ] || exit 0

# Не запускаемся параллельно (cron может наложиться).
exec 9>/tmp/tunnel-ensure.lock
flock -n 9 || exit 0

CODE=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$WEBHOOK_URL/healthz" || true)
if [ "$CODE" = "200" ]; then
  exit 0
fi

echo "$(date '+%F %T') healthz=$CODE — перезапускаю туннель"
./scripts/dev-tunnel.sh || echo "$(date '+%F %T') dev-tunnel неудача — cron повторит через минуту"
