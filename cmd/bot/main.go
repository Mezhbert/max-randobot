// Package main — webhook-сервер MAX-бота для розыгрышей.
// Запуск:  giveawaybot-bot
// Только webhook, никакого long polling.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maxbot/giveawaybot/internal/config"
	"github.com/maxbot/giveawaybot/internal/handlers"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

func main() {
	cfg, err := config.Load(".env", ".env.local")
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	// Уровень логирования.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))
	slog.Info("конфигурация", "info", cfg.String())

	// БД.
	pool, err := store.NewPool(context.Background(), cfg.DBDSN)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if cfg.DBMigrate {
		if err := store.Migrate(context.Background(), pool); err != nil {
			slog.Error("migrate", "err", err)
			os.Exit(1)
		}
	}
	db := store.New(pool)

	// HTTP-клиент с встроенным корневым сертификатом Минцифры
	// (platform-api2.max.ru использует TLS от российского УЦ).
	httpClient, err := config.NewHTTPClient()
	if err != nil {
		slog.Error("http client (CA pool)", "err", err)
		os.Exit(1)
	}

	// MAX API + регистрация webhook.
	api, err := maxapi.New(cfg.BotToken, cfg.MaxRPS, httpClient)
	if err != nil {
		slog.Error("maxapi", "err", err)
		os.Exit(1)
	}

	bot := handlers.New(api, db, slog.Default())

	// Регистрируем webhook при старте (идемпотентно со стороны MAX).
	go func() {
		regCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := api.SubscribeWebhook(regCtx, cfg.FullWebhookURL(), cfg.WebhookSecret); err != nil {
			slog.Warn("subscribe webhook (продолжаю — возможно уже подписан)", "err", err)
		}
	}()

	// Узнаём публичное имя бота — нужно для построения UTM-диплинков (max.ru/<bot>/start/...).
	// Заодно сохраняем userID самого бота (нужен для /clear — фильтр своих сообщений).
	go func() {
		ic, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if info, err := api.Bots.GetMyInfo(ic); err == nil {
			if info.Username != "" {
				bot.SetUsername(info.Username)
				slog.Info("bot username", "name", info.Username)
			}
			if info.UserID != 0 {
				bot.SetBotUserID(info.UserID)
				slog.Info("bot user_id", "id", info.UserID)
			}
		}
	}()

	// HTTP-сервер. Обработчик от библиотеки сам проверяет секрет и метод.
	// ВАЖНО: читаем raw body ДО библиотечного handler, чтобы достать поле
	// "payload" диплинка из bot_started (либа его не парсит). Body подменяем
	// через bytes.Reader, чтобы GetHandler смог разобрать его заново.
	webhookHandler := api.GetHandler(func(ctx context.Context, u model.Update) {
		// Асинхронная обработка, чтобы всегда быстро отвечать 200.
		go bot.Handle(context.Background(), u)
	}, cfg.WebhookSecret)

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))

		// Если это bot_started с payload диплинка (utm_X) — фиксируем клик UTM.
		if payload := maxapi.ParseStartedPayload(body); payload != "" {
			// Достаём user_id тем же парсингом.
			go bot.OnDeepLinkStart(context.Background(), body, payload)
		}

		webhookHandler.ServeHTTP(w, r)
		slog.Debug("webhook", "took", time.Since(start))
	})
	// healthcheck (без проверки секрета).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		slog.Info("HTTP сервер слушает", "port", cfg.HTTPPort, "path", cfg.WebhookPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("завершение...")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}
