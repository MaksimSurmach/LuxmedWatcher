package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/maksimsurmach/luxmed-watcher/internal/app"
)

func main() {
	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := app.New(cfg, logger).Run(context.Background()); err != nil {
		logger.Error("app stopped", "err", err)
		os.Exit(1)
	}
}
