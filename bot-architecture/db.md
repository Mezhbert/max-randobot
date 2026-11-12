# База данных: схема и как всё DB-wise работает

PostgreSQL 16 (pgx/v5), миграции встроены в бинарник (`internal/store/migrations/`, `go:embed`), применяются автоматически при старте (`DB_MIGRATE=true`). Идемпотентны (`IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`).

## ER-диаграмма (все таблицы)

```mermaid
erDiagram
    users ||--o{ channels : "владеет (owner_user_id)"
    users ||--o{ giveaways : "создаёт (owner_user_id)"
    users ||--o{ participants : "участвует"
    users |o--o{ participants : "пригласил (referral_by)"
    giveaways ||--o{ participants : ""
    participants ||--o{ subscriptions_log : ""
    giveaways ||--o{ subscriptions_log : ""
    channels ||--o{ subscriptions_log : ""
    giveaways ||--o{ referral_links : ""
    users ||--o{ referral_links : "личный код"
    giveaways ||--o{ referral_invites : ""
    users ||--o| user_sessions : "текущее состояние"
    users ||--o| owners : "тариф"
    users ||--o{ utm_links : "владеет"
    channels |o--o{ utm_links : "куда ведёт"
    utm_links ||--o{ utm_clicks : "каждый клик"
    utm_links ||--o{ user_utm_attribution : "последний источник юзера"
    users ||--o| user_utm_attribution : "атрибут"

    users {
        bigint user_id PK
        text first_name
        text username
        bool is_bot
        bool flag_in_base "можно ли боту писать (bot_started/bot_stopped)"
        bigint dialog_chat_id
    }
    channels {
        bigint id PK
        bigint owner_user_id FK
        bigint chat_id "id чата в MAX (может быть отрицательным)"
        text title
        text link "username/слаг для публичной ссылки"
        bool is_bot_admin "кэш; сверяется живым запросом"
    }
    giveaways {
        bigint id PK
        bigint owner_user_id FK
        text name
        text post_text
        jsonb media_tokens "[{token,type}]"
        text button_text
        bigint_array subscribe_channel_ids "→ channels.id"
        bigint_array publish_channel_ids "→ channels.id"
        bool publish_now "режим «сразу»"
        timestamptz publish_at
        text finish_mode "time | count"
        timestamptz finish_at
        int finish_participants_count
        int winners_count "1..100"
        text winners_message
        bool referral_enabled
        int referral_limit
        enum captcha_mode "none | all"
        bool results_in_original_post
        text results_post_text
        text draft_step "текущий шаг FSM (для draft)"
        enum status "draft|scheduled|active|finished|cancelled"
        jsonb publish_message_ids "{chat_id: mid} постов"
        jsonb results_message_ids "{chat_id: mid} итогов"
        bigint_array winners_user_ids
        bigint_array winners_notified_user_ids
        bool results_published
        int publish_attempts "кап ретраев (10 → cancelled)"
    }
    participants {
        bigint id PK
        bigint giveaway_id FK
        bigint user_id FK
        bigint referral_by FK "кто пригласил"
        int tickets "1 + приглашённые (в пределах лимита)"
        bool captcha_passed
    }
    subscriptions_log {
        bigint id PK
        bigint participant_id FK
        bigint giveaway_id FK
        bigint channel_id FK
        bigint user_id
        timestamptz subscribed_at
        timestamptz unsubscribed_at "NULL = не отписался"
        text sub_source "post | utm"
    }
    referral_links {
        bigint giveaway_id FK
        bigint user_id FK
        text code UK "ref_<code> — личная ссылка"
    }
    referral_invites {
        bigint giveaway_id
        bigint inviter_user_id
        bigint invitee_user_id FK
    }
    user_sessions {
        bigint user_id PK
        text state "adding_channel|utm_create|captcha_verify|edit_field|..."
        jsonb payload "контекст состояния"
    }
    owners {
        bigint user_id PK
        text tier "free | pro | business"
    }
    utm_links {
        bigint id PK
        bigint owner_user_id FK
        bigint channel_id FK "NULL = легаси-ссылка без канала"
        text source "НАЗВАНИЕ ссылки (из быстрого флоу/билдера)"
        text medium "всегда referral"
        text post_ref "ссылка max.ru/... или метка размещения"
        numeric cost "₽, для CPA"
        text code UK "u_<code> в startapp-ссылке"
        bigint clicks
        bigint signups
        bigint unsubs
        bool enabled
    }
    utm_clicks {
        bigint id PK
        bigint utm_link_id FK
        bigint user_id "0 = анонимный клик (/r/)"
        timestamptz started_at
    }
    user_utm_attribution {
        bigint user_id PK "последний источник выигрывает"
        bigint utm_link_id FK
    }
```

`feature_limits` (справочник лимитов по тарифам: `tier`, `feature_key`, `limit_value`; `-1`=безлимит) — без FK, каркас под буд. платные функции.

## Почему схема такая

- **Всё в одной БД,FK на `users`** — ТЗ: «все пользователи попадают в базу бота». Юзер появляется из любого события: webhook-апдейт, участие, клик по трекинг-ссылке (мини-апп upsert'ит до записи клика — иначе FK `user_utm_attribution → users` молча откатывал бы клик).
- **Каналы — словарь владельца** (`owner_user_id` + `chat_id`): один канал могут добавить несколько организаторов; `is_bot_admin` — кэш, живой запрос `IsBotAdminInChat` перепроверяет его в критичных местах (шаг публикации, `/refresh`).
- **Каналы в розыгрыше — `bigint[]`, не join-таблица**: список выбирается пикером и правится атомарно одной колонкой. Денормализация осознанная (KISS): каналов в розыгрыше ≤ десятков.
- **`giveaways.publish_message_ids` / `results_message_ids` (jsonb `{chat_id: mid}`)** — идемпотентность публикации: краш между отправкой поста и сменой статуса не дублирует пост при перезапуске.
- **`participants` — единственное место, где живёт участие**; `ON CONFLICT DO NOTHING` даёт идемпотентность повторных кликов. Билеты (`tickets`) — основа взвешенной жеребьёвки.
- **`subscriptions_log.unsubscribed_at NULL = «не отписался»** — прямой ответ на требование ТЗ «через сколько отписался (если нет — nan)»; тикер `CheckUnsubs` обновляет колонку живыми проверками.
- **`user_sessions` — только НЕ-FSM состояния** (добавление канала, билдер ссылок, капча-верификация, редактура поля). Самwizard создания розыгрыша хранится в самой строке `giveaways` (`status='draft'`, `draft_step`) — черновик переживает рестарт и продолжается с того же шага.

## Жизненный цикл статусов розыгрыша (как БД гарантирует «ровно один раз»)

```mermaid
stateDiagram-v2
    [*] --> draft: StartDraft
    draft --> scheduled: Принять (Confirm)
    scheduled --> publishing: claim (атомарный UPDATE…WHERE status='scheduled')
    publishing --> active: посты отправлены (MarkPublished)
    publishing --> scheduled: ошибка (revert)
    scheduled --> cancelled: владелец отменил
    active --> finishing: claim (по finish_at ИЛИ finish_participants_count)
    finishing --> finished: победители+итоги (MarkFinished)
    finishing --> active: ошибка (revert)
    active --> cancelled: 10 неудач публикации
```

Атомарный claim (`UPDATE giveaways SET status='publishing' WHERE id=$1 AND status='scheduled'`) — двойной тик планировщика или параллельный cron-процесс не переотправят пост. Зависший в `publishing/finishing` дольше 5 минут розыгрыш откатывается (`recoverStale`). После выбора победителей (`winners_user_ids`) шаги идемпотентны по флагам: `winners_notified_user_ids` (кому ушло ЛС), `results_published` (вышли ли итоги).

## Как каждая функция трогает таблицы

| Функция | Читает | Пишет |
|---|---|---|
| Создание розыгрыша (FSM) | `channels`, `user_sessions` | `giveaways` (одна строка-черновик, `draft_step`), `user_sessions` |
| Участие (мини-апп/чат) | `giveaways`, `participants`, `subscriptions_log` | `participants` (+`tickets` при реферальном кредите), `subscriptions_log`, `referral_invites` |
| Итоги | `participants` (verified), `winners_*` | `giveaways` (winners, results_published, статусы) |
| Трекинг-клик | `utm_links` | `users` (upsert), `utm_clicks`, `utm_links.clicks`, `user_utm_attribution` |
| Подписка в канале | `user_utm_attribution` | `utm_links.signups`, `subscriptions_log`, `utm_subscribers` (retention) |
| Отписка | `user_utm_attribution` | `utm_links.unsubs`, `subscriptions_log.unsubscribed_at`, `utm_subscribers.unsubscribed_at`, `giveaway`-дисквалификация |
| Суточный поллинг удержания | `user_utm_attribution` × `utm_subscribers` | `utm_subscribers` (добавление уже-состоящих, фиксация тихих отписок) |
| Рефералка | `referral_links`, `referral_invites` | `referral_links`, `referral_invites`, `participants.tickets` |
| Редактура | `giveaways` | `giveaways` (точечные колонки, live-обновление постов) |
| Аналитика/CSV | `participants`, `subscriptions_log`, `utm_links`, `utm_clicks` | — |

## Индексы (что и почему)

| Индекс | Зачем |
|---|---|
| `giveaways (owner_user_id, status)` | списки «Мои розыгрыши» по фильтру |
| `giveaways (status, publish_at) WHERE status='scheduled'` | тикер публикации — только наставшие |
| `giveaways (status, finish_at) WHERE status='active'` | тикер итогов |
| `giveaways (status) WHERE finish_mode='count' AND finish_participants_count>0` | розыгрыши «по количеству участников» |
| `giveaways (updated_at) WHERE status IN ('publishing','finishing')` | откат зависших захвата (два частичных индекса) |
| `participants (giveaway_id)` / `(user_id)` | счётчик кнопки, «Мои участия», жеребьёвка |
| `subscriptions_log (user_id)`, `(channel_id)` | проверка отписок, динамика канала |
| `utm_links (owner_user_id)`, `(channel_id)`; `utm_clicks (utm_link_id)`, `(user_id)` | статистика и атрибуция |
