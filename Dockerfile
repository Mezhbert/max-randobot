# syntax=docker/dockerfile:1
# Multi-stage сборка MAX-бота для розыгрышей.
#
# ВАЖНО: корневой сертификат Минцифры (Russian Trusted Root CA) встроен в
# бинарник через //go:embed (internal/config/certs/). Поэтому SSL_CERT_FILE
# НЕ нужен — TLS к platform-api2.max.ru работает из коробки в любом образе.
FROM golang:1.25-alpine AS builder

WORKDIR /src
# Кэш модулей.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Статические бинарники (CGO_OFF для alpine/musl).
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/giveawaybot-bot ./cmd/bot \
 && go build -trimpath -ldflags="-s -w" -o /out/giveawaybot-cron ./cmd/cron

# --- runtime ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app
ENV TZ=Europe/Moscow
COPY --from=builder /out/giveawaybot-bot   /usr/local/bin/giveawaybot-bot
COPY --from=builder /out/giveawaybot-cron  /usr/local/bin/giveawaybot-cron

USER app
# По умолчанию запускаем webhook-сервер.
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/giveawaybot-bot"]
