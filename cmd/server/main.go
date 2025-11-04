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

type appRunner interface {
	Run(context.Context) error
}

var (
	loadConfig  = config.Load
	newApp      = func(cfg config.Config, logger *slog.Logger) appRunner { return app.New(cfg, logger) }
	signalFuncs = signal.NotifyContext
	exit        = os.Exit
)

func main() {
	logger := newLogger()

	ctx, cancel := signalFuncs(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := run(ctx, logger)
	exit(code)
}

func run(ctx context.Context, logger *slog.Logger) int {
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("failed to load configuration", slog.Any("error", err))
		return 1
	}

	if err := newApp(cfg, logger).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server failed", slog.Any("error", err))
		return 1
	}

	logger.Info("shutdown complete")
	return 0
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
