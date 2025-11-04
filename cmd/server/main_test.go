package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/shini4i/argo-watcher-mcp/internal/config"
)

type stubRunner struct {
	err error
	ctx context.Context
}

func (s *stubRunner) Run(ctx context.Context) error {
	s.ctx = ctx
	return s.err
}

func TestRunSuccess(t *testing.T) {
	origLoad := loadConfig
	origNew := newApp
	origSignals := signalFuncs
	t.Cleanup(func() {
		loadConfig = origLoad
		newApp = origNew
		signalFuncs = origSignals
	})

	loadConfig = func() (config.Config, error) {
		return config.Config{}, nil
	}

	runner := &stubRunner{}
	newApp = func(cfg config.Config, logger *slog.Logger) appRunner {
		return runner
	}

	signalFuncs = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
		return context.WithCancel(ctx)
	}

	logger := newLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	code := run(ctx, logger)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if runner.ctx == nil {
		t.Fatal("expected runner to receive context")
	}
}

func TestRunConfigError(t *testing.T) {
	origLoad := loadConfig
	t.Cleanup(func() { loadConfig = origLoad })

	loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("load failed")
	}

	code := run(context.Background(), newLogger())
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

func TestRunAppErrorPropagation(t *testing.T) {
	origLoad := loadConfig
	origNew := newApp
	t.Cleanup(func() {
		loadConfig = origLoad
		newApp = origNew
	})

	loadConfig = func() (config.Config, error) {
		return config.Config{}, nil
	}

	newApp = func(config.Config, *slog.Logger) appRunner {
		return &stubRunner{err: errors.New("boom")}
	}

	if code := run(context.Background(), newLogger()); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

func TestRunTreatsContextCanceledAsSuccess(t *testing.T) {
	origLoad := loadConfig
	origNew := newApp
	t.Cleanup(func() {
		loadConfig = origLoad
		newApp = origNew
	})

	loadConfig = func() (config.Config, error) {
		return config.Config{}, nil
	}

	runner := &stubRunner{err: context.Canceled}
	newApp = func(config.Config, *slog.Logger) appRunner {
		return runner
	}

	if code := run(context.Background(), newLogger()); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

func TestNewLoggerUsesJSONHandler(t *testing.T) {
	logger := newLogger()
	if _, ok := logger.Handler().(*slog.JSONHandler); !ok {
		t.Fatalf("expected JSON handler, got %T", logger.Handler())
	}
}

func TestMainUsesExitCode(t *testing.T) {
	origLoad := loadConfig
	origNew := newApp
	origSignals := signalFuncs
	origExit := exit
	t.Cleanup(func() {
		loadConfig = origLoad
		newApp = origNew
		signalFuncs = origSignals
		exit = origExit
	})

	loadConfig = func() (config.Config, error) {
		return config.Config{}, nil
	}

	newApp = func(config.Config, *slog.Logger) appRunner {
		return &stubRunner{}
	}

	signalFuncs = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		return ctx, cancel
	}

	var code int
	exit = func(c int) { code = c }

	main()

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}
