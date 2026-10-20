# Развёртывание (без Cloudflare)

Два сценария: **локальная разработка на Ubuntu** (SSH-туннель) и **прод на
любом VPS** (свой домен + Caddy с авто-TLS). Cloudflare не нужен нигде.

---

## Общие требования

- Linux (тестировалось на Ubuntu), Docker + Docker Compose plugin
- Токен бота из @MasterBot (MAX)
- `.env` в корне проекта: `cp .env.example .env` и заполнить
  `BOT_TOKEN`, `WEBHOOK_SECRET` (длинная случайная строка)

`WEBHOOK_URL` зависит от сценария — см. ниже.

---

## Сценарий A — локальная разработка (Ubuntu, без домена)

Бот и БД крутятся локально в docker; наружу смотрит временный SSH-туннель
[localhost.run](https://localhost.run) (HTTPS из коробки, не зависит от
блокировок trycloudflare).

```bash
docker compose up -d --build   # postgres + бот
./scripts/dev-tunnel.sh        # поднимет туннель, пропишет WEBHOOK_URL, рестартует бота
```

Скрипт сам: убивает старый туннель → поднимает новый → получает URL вида
`https://xxxx.lhr.life` → обновляет `.env` → `docker compose up -d
--force-recreate bot` (бот переподписывает webhook в MAX).

Проверка: `docker compose logs -f bot` — строка `webhook подписан ... success=true`.

Нюансы:
- URL **временный**: перезапустил скрипт/оборвалась сеть — URL новый,
  повтори `./scripts/dev-tunnel.sh`. Для постоянного домена бесплатно:
  [localhost.run forever-free](https://localhost.run/docs/forever-free/)
  (SSH-ключ → стабильный поддомен).
- Смена токена бота: правишь `BOT_TOKEN` в `.env` →
  `docker compose up -d --force-recreate bot`. Помни: это новый бот для
  платформы — старые посты/каналы к нему отношения больше не имеют.

---

## Сценарий B — продакшен на VPS (свой домен, авто-TLS)

Стек: `postgres + bot + caddy` (см. `docker-compose.prod.yml`). Caddy сам
выпускает и продлевает сертификат Let's Encrypt на портах 80/443.

### 1. DNS

A-запись домена (или поддомена) → публичный IP сервера:

```
bot.example.com.  300  IN  A  <IP сервера>
```

### 2. Файрвол

```bash
sudo ufw allow 22/tcp && sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw enable
```

### 3. .env

```
BOT_TOKEN=<токен>
WEBHOOK_SECRET=<длинная случайная строка>
WEBHOOK_URL=https://bot.example.com
```

### 4. Запуск

```bash
DOMAIN=bot.example.com ACME_EMAIL=you@mail.com \
  docker compose -f docker-compose.prod.yml up -d --build
```

`DOMAIN`/`ACME_EMAIL` можно положить в `.env` — compose их увидит.

### 5. Проверка

```bash
curl https://bot.example.com/healthz          # → ok
docker compose -f docker-compose.prod.yml logs bot | grep webhook
# → "webhook подписан url=https://bot.example.com/webhook success=true"
```

---

## Обслуживание

```bash
# Логи
docker compose -f docker-compose.prod.yml logs -f bot

# Обновление (после git pull)
docker compose -f docker-compose.prod.yml up -d --build

# Бэкап БД (кладёт дамп в ./backup.sql)
docker compose -f docker-compose.prod.yml exec -T postgres \
  pg_dump -U giveaway giveaway > backup.sql

# Восстановление
cat backup.sql | docker compose -f docker-compose.prod.yml exec -T postgres \
  psql -U giveaway -d giveaway
```

---

## Частые вопросы

**TLS к платформе MAX (сертификат Минцифры)?** Ничего настраивать не надо:
Russian Trusted Root CA встроен в бинарник (`internal/config/certs/`,
`config.NewHTTPClient`). `SSL_CERT_FILE` не требуется. Входящий TLS
(webhook) закрывает Caddy/Let's Encrypt.

**Почему не Nginx + certbot?** Можно и так — Caddy выбран за автоматический
сертификат без cron-продлений. Боту без разницы, что перед ним: любой
реверс-прокси, проксирующий на `bot:8080` (или `127.0.0.1:8080`).

**443 занят другим сайтом?** Проксируй `bot.example.com` на
`127.0.0.1:8080` из своего основного прокси и не поднимай caddy-сервис
(`docker compose -f docker-compose.prod.yml up -d --build postgres bot`).

**Let's Encrypt недоступен с сервера?** (нестандартные сети/РФ-блокировки) —
выпиши сертификат любым доступным способом (например, DNS-01-челлендж через
acme.sh) и положи в Caddy через `tls /path/cert.pem /path/key.pem`,
либо используй сценарий A с постоянным localhost.run-доменом.
