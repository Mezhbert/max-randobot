#!/usr/bin/env bash
# ============================================================================
# dev-tunnel.sh — публичный HTTPS-туннель для ЛОКАЛЬНОЙ разработки (Ubuntu)
# без Cloudflare: SSH-туннель localhost.run (works where trycloudflare blocked).
#
# Что делает:
#   1. Поднимает SSH-туннель  https://<random>.lhr.life  →  localhost:8080
#   2. Прописывает URL в .env (WEBHOOK_URL=...)
#   3. Пересоздаёт контейнер бота (он переподписывает webhook в MAX)
#
# Использование:  ./scripts/dev-tunnel.sh
# URL временный: при каждом запуске новый (анонимный localhost.run).
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

LOG=/tmp/lhr-tunnel.log

# Убиваем прошлый туннель (если был) — URL всё равно новый.
pkill -f "ssh.*localhost\.run" 2>/dev/null || true
sleep 1

setsid nohup ssh \
  -o StrictHostKeyChecking=no \
  -o BatchMode=yes \
  -o ServerAliveInterval=60 \
  -o ExitOnForwardFailure=yes \
  -R 80:localhost:8080 \
  nokey@localhost.run > "$LOG" 2>&1 < /dev/null &

# Ждём, пока сервер пришлёт назначенный домен.
URL=""
for _ in $(seq 1 15); do
  sleep 1
  URL=$(sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' "$LOG" \
        | grep -oE 'https://[a-z0-9-]+\.lhr\.life' | head -1 || true)
  [ -n "$URL" ] && break
done

if [ -z "$URL" ]; then
  echo "ОШИБКА: туннель не поднялся, лог: $LOG" >&2
  tail -5 "$LOG" >&2
  exit 1
fi

echo "Туннель: $URL"

# Проверяем, что он реально проксирует.
CODE=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$URL/healthz" || true)
if [ "$CODE" != "200" ]; then
  echo "ПРЕДУПРЕЖДЕНИЕ: /healthz через туннель вернул '$CODE' (бот запущен? docker compose up -d)"
fi

# Обновляем .env и рестартуем бота (переподписка webhook на новый URL).
if [ -f .env ] && grep -q '^WEBHOOK_URL=' .env; then
  sed -i "s|^WEBHOOK_URL=.*|WEBHOOK_URL=$URL|" .env
  echo "WEBHOOK_URL обновлён, пересоздаю бота..."
  docker compose -f docker-compose.yml up -d --force-recreate bot
  echo "Готово. Webhook: $URL/webhook"
else
  echo "Файл .env не найден — пропиши WEBHOOK_URL=$URL вручную."
fi
