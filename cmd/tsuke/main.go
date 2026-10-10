// Command tsuke is a Telegram bot for entering transactions into Firefly III.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // time zones work in minimal images

	"github.com/Mimic890/tsuke/internal/bot"
	"github.com/Mimic890/tsuke/internal/config"
	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/logx"
	"github.com/Mimic890/tsuke/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tsuke:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(os.Stderr, cfg.LogLevel, cfg.TelegramToken, cfg.FireflyToken, cfg.WebhookSecret)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ff := firefly.New(cfg.FireflyURL, cfg.FireflyToken, log)
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := ff.Ping(pingCtx); err != nil {
		// Not fatal: Firefly may come up later; the cache is restored from disk.
		log.Warn("firefly is not reachable yet", "err", err)
	}
	cancel()

	b, err := bot.New(bot.Options{Config: cfg, Firefly: ff, Store: st, Logger: log})
	if err != nil {
		return err
	}
	log.Info("tsuke starting", "tz", cfg.Location.String(), "users", len(cfg.AllowedUserIDs))
	return b.Run(ctx)
}
