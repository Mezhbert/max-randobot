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
	"github.com/maxbot/giveawaybot/internal/dedup"
	"github.com/maxbot/giveawaybot/internal/handlers"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/scheduler"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/webapp"
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

	// Встроенный планировщик: сам раз в 30 секунд публикует наступившие
	// розыгрыши (scheduled → active) и подводит итоги завершившихся
	// (active → finished). Раньше это делал внешний crontab, который легко
	// забывается настроить — из-за чего розыгрыши не публиковались во время.
	// Встроенный тикер убирает эту зависимость: всё работает в одном процессе.
	sch := scheduler.New(api, db, slog.Default())

	bot := handlers.New(api, db, slog.Default())
	bot.SetDedup(dedup.New(dedupTTL(cfg.DedupTTL), 0))
	bot.SetCounterUpdater(sch.UpdateParticipantCounter)
	bot.SetPublishNow(sch.PublishGiveawayNow)
	bot.SetHelpURL(cfg.WebhookURL + "/app/help.html")

	schedCtx, schedStop := context.WithCancel(context.Background())
	defer schedStop()
	go runScheduler(schedCtx, sch)

	// Регистрируем webhook при старте (идемпотентно со стороны MAX).
	go func() {
		regCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := api.SubscribeWebhook(regCtx, cfg.FullWebhookURL(), cfg.WebhookSecret); err != nil {
			slog.Warn("subscribe webhook (продолжаю — возможно уже подписан)", "err", err)
		}
	}()

	// Узнаём публичное имя бота — нужно для построения UTM-диплинков.
	// Заодно регистрируем меню команд бота (PatchCommands), чтобы при вводе «/»
	// в MAX появлялся список доступных команд, а не «команды не найдены».
	go func() {
		ic, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if info, err := api.Bots.GetMyInfo(ic); err == nil && info.Username != "" {
			bot.SetUsername(info.Username)
			slog.Info("bot username", "name", info.Username)
		}
		if err := api.RegisterCommands(ic); err != nil {
			slog.Warn("register bot commands (продолжаю)", "err", err)
		} else {
			slog.Info("bot commands зарегистрированы")
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
		// Секрет проверяем ДО любого разбора body: раньше ParseStartedPayload
		// запускал обработку диплинка до того, как библиотечный handler
		// проверял X-Max-Bot-Api-Secret — подделанный bot_started регистрировал
		// участников без подписи. Здесь — граница доверия, дальше всё аутентично.
		if secret := r.Header.Get("X-Max-Bot-Api-Secret"); secret == "" || secret != cfg.WebhookSecret {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		// Лимит тела: апдейты MAX — килобайты; гигабайтный POST не должен
		// съедать память процесса.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))

		// bot_started с payload диплинка (part_/utm_): payload недоступен в
		// SDK-модели Update — запоминаем СИНХРОННО до передачи body в SDK-хендлер,
		// чтобы onBotStarted в Handle забрал его без гонки (и не слал стартовый
		// экран поверх целевого сообщения — юзер «провалился» в диалог по кнопке).
		if payload := maxapi.ParseStartedPayload(body); payload != "" {
			if uid := maxapi.ParseStartedUserID(body); uid > 0 {
				bot.SetPendingStart(uid, payload)
			}
		}

		webhookHandler.ServeHTTP(w, r)
		slog.Debug("webhook", "took", time.Since(start))
	})
	// healthcheck (без проверки секрета).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Метрики RPS/ошибок по API MAX в формате Prometheus text.
	// Pull-модель: `curl /metrics` работает из коробки; внешний скрейпер
	// (Prometheus/Grafana) подключается опционально — нагрузки на платформу
	// это не создаёт, считаются локальные атомарные счётчики.
	mux.HandleFunc(cfg.MetricsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		api.Metrics().WritePrometheus(w)
	})
	// Мини-апп: только главный экран с описанием (+ справка для «Поддержка»).
	mux.Handle("/app/", webapp.Handler())

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

	schedStop() // остановить тикер планировщика

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// runScheduler — встроенный цикл планировщика. Каждые 30 секунд:
//   - публикует scheduled-розыгрыши с наступившим publish_at (→ active);
//   - подводит итоги active-розыгрышей с наступившим finish_at (→ finished).
//
// Запускается отдельной горутиной, останавливается по отмене ctx.
func runScheduler(ctx context.Context, sch *scheduler.Scheduler) {
	const interval = 30 * time.Second
	// Сразу один проход при старте — чтобы «догнать» пропущенные публикации
	// (например, бот был выключен в момент publish_at).
	tick := func() {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := sch.PublishPending(c); err != nil {
			slog.Warn("scheduler PublishPending", "err", err)
		}
		if err := sch.FinishPending(c); err != nil {
			slog.Warn("scheduler FinishPending", "err", err)
		}
	}
	tick()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// dedupTTL парсит DEDUP_TTL (duration, напр. "5m", "120s"); пусто — дефолт.
func dedupTTL(s string) time.Duration {
	if s == "" {
		return dedup.DefaultTTL
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return dedup.DefaultTTL
}
