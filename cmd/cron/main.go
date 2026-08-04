// Package main — CLI для cron-задач бота.
//
// Использование (внешний cron в Europe/Moscow):
//
//	# Каждую минуту: публиковать наступившие розыгрыши
//	* * * * * giveawaybot-cron publish
//	# Каждую минуту: подводить итоги завершившихся розыгрышей
//	* * * * * giveawaybot-cron finish
//	# Раз в 10 минут: перепроверять отписки участников
//	*/10 * * * * giveawaybot-cron unsub-check
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/maxbot/giveawaybot/internal/config"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/scheduler"
	"github.com/maxbot/giveawaybot/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: giveawaybot-cron <publish|finish|unsub-check>")
		os.Exit(2)
	}
	cmd := os.Args[1]

	cfg, err := config.Load(".env", ".env.local")
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	pool, err := store.NewPool(context.Background(), cfg.DBDSN)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	db := store.New(pool)

	httpClient, err := config.NewHTTPClient()
	if err != nil {
		slog.Error("http client (CA pool)", "err", err)
		os.Exit(1)
	}

	api, err := maxapi.New(cfg.BotToken, cfg.MaxRPS, httpClient)
	if err != nil {
		slog.Error("maxapi", "err", err)
		os.Exit(1)
	}

	sch := scheduler.New(api, db, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch cmd {
	case "publish":
		err = sch.PublishPending(ctx)
	case "finish":
		err = sch.FinishPending(ctx)
	case "unsub-check":
		err = sch.CheckUnsubs(ctx)
	default:
		fmt.Fprintln(os.Stderr, "неизвестная команда:", cmd)
		os.Exit(2)
	}

	if err != nil {
		slog.Error("cron", "cmd", cmd, "err", err)
		os.Exit(1)
	}
	slog.Info("cron ok", "cmd", cmd)
}
