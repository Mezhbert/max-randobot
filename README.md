# 🎁 MAX-бот для розыгрышей (Go, webhook-only)

Бот для проведения розыгрышей в мессенджере **MAX**. Только **webhook** (никакого
long polling), PostgreSQL, **встроенный планировщик** публикации/итогов с атомарным
захватом задач (без внешнего cron).

Использует официальный клиент [max-messenger/max-bot-api-client-go](https://github.com/max-messenger/max-bot-api-client-go)
и следует [документации MAX для разработчиков](https://dev.max.ru/docs/chatbots/bots-coding/prepare).

---

## Возможности

### Основной функционал
- **Главное меню**: Создать розыгрыш · Мои розыгрыши · Мои каналы · Мои участия · Поддержка.
- **Цепочка создания розыгрыша (13 шагов ТЗ)** с кнопками **«← Назад»** и **«✖ Отменить»** на каждом шаге.
- Типы розыгрышей: текущие / запланированные / в процессе создания (черновики) / завершённые / отменённые.
- **Встроенный планировщик**: публикация поста и подведение итогов по расписанию
  выполняются самим ботом (каждые 30 сек), с **атомарным захватом** через
  промежуточные статусы `publishing`/`finishing` — никакой внешний cron не нужен.
- **Проверка подписок** на каналы через `GET /chats/{id}/members?user_ids=...` — один
  запрос на проверку конкретного участника, в рамках лимита платформы 30 rps.
- Даты по **московскому времени** (формат `ДД.ММ.ГГГГ ЧЧ:ММ`).
- Безопасность webhook через заголовок `X-Max-Bot-Api-Secret`.
- **Дедупликация** webhook-апдейтов (защита от ретраев MAX → дублей ответов).
- **Мониторинг RPS**: счётчики вызовов MAX API по методу/исходу + gauge «rps» через `/metrics`.

### Управление каналами
- **«Мои каналы»** → список добавленных каналов с кнопкой аналитики/UTM на каждый.
- **Добавление канала**: по пересланному сообщению из канала либо по числовому ID.
  Бот проверяет, что он админ в канале (`CheckChannelAdmin`), и только тогда сохраняет.
- **Выбор каналов в FSM**: на шагах «каналы подписки» / «каналы публикации» рендерятся
  чекбоксы (✅/⬜) с живым переключением — больше не нужно вводить ID вручную.

### UTM-трекинг источников подписчиков 🎯
Полноценная аналитика притока подписчиков по каналам, где бот — админ:
- **Создание UTM-ссылки**: «Мои каналы» → выбрать канал → «UTM-ссылки» →
  «Создать». Формат ввода: `источник | кампания | пост` (например
  `telegram | launch | post_42`).
- Генерируется **диплинк** вида `https://max.ru/<bot>/start/utm_<code>`.
- **Клик по диплинку** → `bot_started` с `payload` → фиксируется клик (来源, кампания).
  Обход ограничения SDK: payload читается из raw-тела webhook ДО библиотечного handler.
- **Подписка по ссылке** → `user_added` с `inviter_id` → атрибуция: клик → регистрация,
  связывается с конкретным каналом и UTM-ссылкой.
- **Отписка** → `user_removed` → списание из статистики UTM.
- **Аналитика**: «Статистика UTM» — клики / регистрации / конверсия / отписки по ссылке.
- **Экспорт CSV**: «Экспорт CSV» — выгрузка всех метрик по UTM-ссылкам канала.
- Схема: миграция `0002_utm_tracking.sql` (`utm_links`, `utm_clicks`,
  `user_utm_attribution`, расширение `subscriptions_log`).

### Тарифы (фундамент, без блокировок)
Закладываем схему и middleware-хелперы под буд. платный/бесплатный функционал:
- `owners` — создатели розыгрышей с тарифом (`free` / `pro` / `business`).
- `feature_limits` — справочник лимитов по тарифам (`max_channels`, `utm_links`,
  `referral`, `promotion`; `-1` = безлимит, `0` = выключено, `>0` = число).
- API в `store/owners.go`: `EnsureOwner`, `GetOwnerTier`, `SetOwnerTier`,
  `FeatureLimit`, `HasFeature`. Блокировок пока нет — только каркас.
- Миграция: `0003_tiers.sql`.

### Сертификат Минцифры — встроен в сборку 🔒
Платформа MAX (`platform-api2.max.ru`) использует TLS-сертификат, подписанный
российским УЦ (Минцифры), которого нет в системных хранилищах Debian/Alpine.
Решение: публичный корневой сертификат **встроен в бинарник** через `//go:embed`
(`internal/config/certs/`). Программно добавляется в CA-pool кастомного
`*http.Client`, который передаётся в SDK через `maxbot.WithHTTPClient`.
**`SSL_CERT_FILE` больше не нужен** — бот работает из коробки в любом окружении.

---

## Архитектура

```
cmd/bot/      — webhook-сервер (точка входа)
cmd/cron/     — cron-задачи: publish / finish / unsub-check (опционально, основное — встроенный scheduler)
internal/
  config/     — загрузка .env + валидация + TLS (встроенный CA Минцифры)
  maxapi/     — обёртка над max-bot-api-client-go + rate-limit 30 rps + метрики RPS
  store/      — PostgreSQL (pgx/v5) + миграции (embed)
    migrations/  — 0001_init, 0002_utm_tracking, 0003_tiers, 0004/0005_scheduler_claim
  fsm/        — конечный автомат создания розыгрыша (шаги 1-13)
  handlers/   — роутинг update, главное меню, экраны, callback'и, UTM, каналы
  scheduler/  — публикация/итоги по расписанию + атомарный claim + идемпотентность
  draw/       — weighted-выбор победителей (детерминированный, тестируемый)
  dedup/      — дедупликация webhook-апдейтов (TTL map)
  metrics/    — счётчики RPS/ошибок по API MAX + Prometheus text
  subscription/ — проверка подписок через GetMembers(user_ids)
  util/       — даты (МСК), клавиатуры
deploy/
  cloudflared/ — config.yml + SETUP.md (named-tunnel инструкция)
  systemd/     — unit-файлы для бота, cloudflared, cron-timer'ы
```

**Лимит платформы MAX — 30 rps.** Все вызовы в `internal/maxapi` проходят через
token-bucket rate-limiter (`MAX_RPS`, дефолт 28 для запаса) и учитываются в
локальных счётчиках (`internal/metrics`) — без нагрузки на платформу.

---

## Установка Go

Проект требует **Go 1.25+**. Если в системе нет нужной версии — соберите через Docker
(не требует локальной установки):

```bash
# Проверка/сборка через официальный образ (рекомендуется для CI/без-установки)
docker run --rm -v "$PWD:/src" -w /src -e GOFLAGS=-mod=mod golang:1.25-alpine \
  sh -c 'apk add --no-cache git >/dev/null 2>&1; go build ./...'

# Локально (если Go установлен)
go version  # должно быть >= 1.25
go build ./...
```

Также есть `Makefile` с целями `build`, `run`, `test`, `test-unit`, `test-integration`, `test-store`, `vet`.

---

## Тесты

Тесты покрывают все ключевые сценарии (FSM-флоу, participate с подпиской,
гонку публикации, идемпотентность finish, итоги в исходном посте/fallback,
дедуп, weighted-выбор победителей, atomic claim в store).

```bash
# Юнит-тесты (без Docker/сети, мгновенно)
make test-unit

# Интеграционные (нужен Docker — testcontainers поднимает реальный PostgreSQL)
make test-integration

# Только store (atomic claim race, finish идемпотентность, draw)
make test-store

# Всё
make test
```

Для интеграционных тестов используется `go-test:alpine` (Go + git + docker-cli):
```bash
docker build -t go-test:alpine -f /tmp/go-test.Dockerfile .  # один раз
# где /tmp/go-test.Dockerfile:
#   FROM golang:1.25-alpine
#   RUN apk add --no-cache git docker-cli
```

---

## Мониторинг RPS

Метрики вызовов MAX API (без нагрузки на платформу — считаются локально):

```bash
curl http://localhost:8080/metrics
```

Вывод в формате Prometheus text:
```
max_api_calls_total{method="send_text",result="success"} 1234
max_api_calls_total{method="send_text",result="error"} 2
max_api_rps_last_second 5
```

Внешний скрейпер (Prometheus/Grafana) подключается опционально — pull-модель,
path настраивается через `METRICS_PATH` (дефолт `/metrics`).

---

## Конфигурация

Скопируйте `.env.example` → `.env` и заполните:

```bash
cp .env.example .env
```

| Переменная       | Описание                                                |
|------------------|---------------------------------------------------------|
| `BOT_TOKEN`      | Токен бота от MasterBot (НЕ коммитить)                  |
| `WEBHOOK_URL`    | Публичный HTTPS-URL (с 25 мая платформа требует HTTPS)  |
| `WEBHOOK_SECRET` | 5-256 символов; проверяется через `X-Max-Bot-Api-Secret`|
| `HTTP_PORT`      | Локальный порт сервера (по умолчанию 8080)              |
| `WEBHOOK_PATH`   | Путь вебхука (по умолчанию `/webhook`)                  |
| `DB_DSN`         | `postgres://user:pass@host:5432/db?sslmode=disable`     |
| `DB_MIGRATE`     | `true` — применять миграции при старте                  |
| `TZ`             | `Europe/Moscow`                                         |
| `MAX_RPS`        | Лимит запросов к платформе (1-30), дефолт 28            |
| `LOG_LEVEL`      | `debug`/`info`/`warn`/`error`                           |
| `METRICS_PATH`   | Путь для `/metrics` (дефолт `/metrics`)                 |
| `DEDUP_TTL`      | TTL дедупликатора (duration, дефолт `5m`)               |

---

## Деплой на VPS с доменом и HTTPS

Платформа MAX шлёт webhook на **HTTPS**. Два варианта терминирования HTTPS:

### Вариант A: Cloudflare Tunnel (рекомендуется, без открытых портов)

Не нужен ни публичный IP, ни открытый порт, ни nginx. Cloudflare Tunnel (`cloudflared`)
поднимает исходящее соединение к edge — VPS может сидеть за NAT.

Подробная пошаговая инструкция (login → create → route dns → config → systemd) —
в **[`deploy/cloudflared/SETUP.md`](deploy/cloudflared/SETUP.md)**.

Кратко:
```bash
cloudflared tunnel login                           # один раз, браузер
cloudflared tunnel create max-bot                  # создаёт named-туннель
cloudflared tunnel route dns max-bot bot.example.ru # стабильный домен навсегда
# скопировать deploy/cloudflared/config.yml → ~/.cloudflared/config.yml (вписать UUID+домен)
sudo cp deploy/systemd/cloudflared-max-bot.service /etc/systemd/system/
sudo systemctl enable --now cloudflared-max-bot
```

В `.env` ставим `WEBHOOK_URL=https://bot.example.ru` — URL стабилен навсегда,
webhook перерегистрировать при рестартах не нужно.

### Вариант B: nginx + Let's Encrypt (классика)

```
MAX (platform-api2.max.ru) --HTTPS--> nginx/caddy --HTTP--> :8080 (бот)
```

Пример `nginx`:

```nginx
server {
    listen 443 ssl http2;
    server_name giveaway.example.com;
    ssl_certificate     /etc/letsencrypt/live/giveaway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/giveaway.example.com/privkey.pem;

    location /webhook {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_read_timeout 35s;  # MAX ждёт ответ 200 до 30 сек
    }

    location /healthz { return 200 "ok\n"; }
}
```

```bash
sudo certbot --nginx -d giveaway.example.com
```

### Запуск бота (systemd)

Готовые unit-файлы — в `deploy/systemd/`:
- `max-bot.service` — webhook-сервер
- `max-bot-cron@.service` + timer'ы — cron-задачи через systemd

```bash
sudo cp deploy/systemd/max-bot.service /etc/systemd/system/
sudo systemctl enable --now max-bot
sudo journalctl -u max-bot -f
```

### Планировщик (встроенный)

Бот сам публикует и подводит итоги по расписанию — **внешний cron не нужен**.
Встроенный планировщик (`internal/scheduler`) запускается отдельной горутиной и
каждые 30 секунд:
- публикует `scheduled`-розыгрыши с наступившим `publish_at` (→ `active`);
- подводит итоги `active`-розыгрышей с наступившим `finish_at` (→ `finished`).

Атомарный захват через промежуточные статусы `publishing`/`finishing`
(`UPDATE...RETURNING WHERE status=...`) гарантирует, что даже при гонке тиков
или дублирующем внешнем cron пост/ЛС/итоги отправляются **ровно один раз**.
Застрявшие после краша задачи (`staleClaimAge=5мин`) автоматически откатываются.

> `cmd/cron` оставлен как опциональный fallback (например, для `unsub-check`),
> но для `publish`/`finish` он избыточен — встроенный планировщик покрывает всё.

При использовании внешнего cron (опционально, только `unsub-check`) — для `TZ`
нужна `Europe/Moscow`:

```cron
*/10 * * * * /opt/giveawaybot/giveawaybot-cron unsub-check
```

---

## Локальный запуск (dev)

Через docker-compose (PostgreSQL + бот):

```bash
cp .env.example .env
# отредактировать WEBHOOK_URL на ваш ngrok/cloudflare URL
docker compose up --build
```

Для локального webhook нужен публичный HTTPS-туннель к `:8080`:

```bash
# ngrok
ngrok http 8080
# полученный https URL впишите в WEBHOOK_URL, перезапустите бот

# или cloudflare tunnel
cloudflared tunnel --url http://localhost:8080
```

Локально без Docker (нужны Go 1.25+ и PostgreSQL):

```bash
go build -o bin/giveawaybot-bot ./cmd/bot
go build -o bin/giveawaybot-cron ./cmd/cron
./bin/giveawaybot-bot
```

---

## Как это работает (поток)

### Создание и проведение розыгрыша
1. **Пользователь запускает бота** → `bot_started` → бот сохраняет его в БД и
   показывает главное меню.
2. **«Создать розыгрыш»** → создаётся черновик (`status=draft`), ведётся по шагам FSM.
   На каждом шаге — текст-приглашение + клавиатура с кнопками **Назад/Отменить**.
3. **«Принять»** на предпросмотре → валидация обязательных полей → статус
   `scheduled` (если `publish_at` в будущем) или `active` (если уже пора).
4. **Встроенный планировщик** (каждые 30 сек) → атомарно захватывает `scheduled`
   (`→ publishing`), публикует пост в выбранные каналы с кнопкой «Участвовать»,
   сохраняет mid каждого поста, `→ active`.
5. **Участник жмёт «Участвовать»** → бот проверяет подписку на все каналы розыгрыша
   через `GetMembers(user_ids)` → регистрирует участника.
6. **Встроенный планировщик** в `finish_at` → атомарно захватывает (`→ finishing`),
   weighted-выбор `winners_count` победителей → шлёт им ЛС (идемпотентно через
   `winners_notified_user_ids`) → публикует итоги (в исходном посте через
   `EditMessage`, с fallback на отдельный пост при ошибке) → `→ finished`.

### UTM-трекинг (поток атрибуции)
```
[пользователь кликает https://max.ru/<bot>/start/utm_<code>]
   ↓ bot_started с payload="utm_<code>"
   ↓ raw-body парсер (maxapi.ParseStartedPayload) → UTMRecordClick(click++)
[подписывается на канал, где бот админ]
   ↓ user_added webhook (inviter_id)
   ↓ UTMAccountSignup: клик→регистрация, связка с каналом+UTM-ссылкой
[отписывается]
   ↓ user_removed → UTMAccountUnsub
[создатель смотрит аналитику]
   ↓ «Мои каналы» → канал → «Статистика UTM» / «Экспорт CSV»
```

---

## Схема БД

Миграции в `internal/store/migrations/`, применяются автоматически при старте
(`DB_MIGRATE=true`) через встроенный мигратор:

| Миграция | Что создаёт |
|----------|-------------|
| `0001_init.sql` | `users`, `channels`, `giveaways` (все 13 полей ТЗ), `participants`, `subscriptions_log`, `referral_links`, `utm_links` (базовая) |
| `0002_utm_tracking.sql` | `user_sessions`, расширение `utm_links` (owner/medium/post_ref/unsubs/enabled), `utm_clicks`, `user_utm_attribution`, расширение `subscriptions_log` (utm_link_id, inviter_id) |
| `0003_tiers.sql` | `owners` (создатели с тарифом), `feature_limits` (справочник лимитов), `users.tier`, `giveaways.owner_tier` |
| `0004_scheduler_claim.sql` | enum-значения `publishing`/`finishing` для `giveaway_status` (атомарный захват задач) |
| `0005_scheduler_claim.sql` | `winners_notified_user_ids`, `results_published` + индексы для поиска застрявших `publishing`/`finishing` |

---

## Проверка webhook вручную

```bash
# healthcheck (должен вернуть "ok")
curl https://bot.example.ru/healthz

# метрики RPS/ошибок по API MAX (Prometheus text)
curl https://bot.example.ru/metrics

# проверить, что webhook зарегистрирован в MAX (нужен CA Минцифры в системе
# ИЛИ запустить через бота: ./bin/giveawaybot-bot и смотреть лог "webhook подписан")
```

---

## Источники

- [Настройка сценариев бота (MAX для разработчиков)](https://dev.max.ru/docs/chatbots/bots-coding/prepare)
- [POST /subscriptions — Webhook](https://dev.max.ru/docs-api/methods/POST/subscriptions)
- [GET /subscriptions](https://dev.max.ru/docs-api/methods/GET/subscriptions)
- [API ботов MAX](https://dev.max.ru/docs-api)
- [max-bot-api-client-go (Go SDK)](https://github.com/max-messenger/max-bot-api-client-go)
