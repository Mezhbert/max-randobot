#!/usr/bin/env bash
# ============================================================================
# update.sh — обновление бота НА СЕРВЕРЕ одной командой.
#   git pull → пересборка образа → рестарт контейнеров (миграции БД
#   применяются автоматически при старте бота, DB_MIGRATE=true).
#
# Использование (на сервере, в корне проекта):
#   ./scripts/update.sh
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== 1/3 git pull =="
git pull --ff-only

echo "== 2/3 пересборка и рестарт =="
docker compose -f docker-compose.prod.yml up -d --build

echo "== 3/3 статус =="
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs --tail 10 bot

echo ""
echo "Готово. Проверка: curl https://<домен>/healthz  → ok"
