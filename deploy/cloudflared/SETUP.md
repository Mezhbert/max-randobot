# Постоянный (named) Cloudflare Tunnel для MAX-бота

Быстрый (quick) туннель `trycloudflare.com` живёт только пока запущен `cloudflared`
и даёт случайный URL — после рестарта он меняется, и webhook в MAX приходится
перерегистрировать. Named-туннель решает это: URL стабилен навсегда, туннель
перезапускается автоматически через systemd.

> **Почему это не делается автоматически.** `cloudflared login` открывает браузер
> и просит авторизоваться в аккаунте Cloudflare и выбрать зону (домен). Это
> интерактивное действие — делегируем его тебе.

## Что нужно заранее

1. Аккаунт Cloudflare с добавленным доменом (зоной). Бесплатно:
   регистрируешь домен где угодно, в Cloudflare добавляешь зону, меняешь NS.
2. `cloudflared` установлен (`cloudflared --version`). На этой машине уже есть:
   `/home/ihb-minion003/.local/bin/cloudflared`.

## Шаг 1. Авторизация (один раз)

```bash
cloudflared tunnel login
```

Откроется браузер → выбираешь свой домен → Cloudflare пишет
"You have successfully logged in". В `~/.cloudflared/` появится `cert.pem`.

## Шаг 2. Создание туннеля

```bash
cloudflared tunnel create max-bot
```

Вывод даст **Tunnel UUID** и путь к credentials-файлу
`~/.cloudflared/<UUID>.json`. Запиши UUID.

## Шаг 3. Привязка DNS-имени

Подставь свой домен (например `bot.example.ru`):

```bash
cloudflared tunnel route dns max-bot bot.example.ru
```

Cloudflare автоматически создаст CNAME-запись
`bot.example.ru → <UUID>.cfargotunnel.com`.

## Шаг 4. Конфиг

Открой `deploy/cloudflared/config.yml`, замени:
- `<TUNNEL_UUID>` — UUID из шага 2;
- `<ВАШ-ДОМЕН>` — домен из шага 3.

Скопируй в место по умолчанию:

```bash
cp deploy/cloudflared/config.yml ~/.cloudflared/config.yml
```

## Шаг 5. Проверка вручную

```bash
cloudflared tunnel run max-bot
```

Открой `https://bot.example.ru/healthz` — должно вернуть `ok`.

## Шаг 6. Автозапуск (systemd)

```bash
# Подставь своего юзера в путь User=, если отличается.
sudo cp deploy/systemd/cloudflared-max-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cloudflared-max-bot
sudo systemctl status cloudflared-max-bot
```

## Шаг 7. Перенацелить бота на постоянный URL

В `.env`:

```dotenv
WEBHOOK_URL=https://bot.example.ru
```

Перезапусти бота — он сам перерегистрирует webhook в MAX на новый стабильный URL.

## Откат к quick-туннелю (временно)

Если домен ещё не готов, можно продолжать на quick-туннеле:

```bash
cloudflared tunnel --no-autoupdate --protocol http2 --url http://localhost:8080
```

и в `.env` ставить выданный `https://....trycloudflare.com`.
