package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shini4i/argo-watcher-mcp/internal/app"
	"github.com/shini4i/argo-watcher-mcp/internal/config"
)

func main() {
	logger := newLogger()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := app.New(cfg, logger).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server failed", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("shutdown complete")
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
