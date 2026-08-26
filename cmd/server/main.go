// Command server runs the orientation and enrollment platform.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vance1852/orientation-enrollment-platform/internal/app"
	"github.com/vance1852/orientation-enrollment-platform/internal/config"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "orientation server stopped: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)
	logger.Info("starting orientation platform", "config", cfg.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instance, err := app.Build(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := instance.Close(); closeErr != nil {
			logger.Error("closing database failed", "error", closeErr.Error())
		}
	}()

	if err := instance.Run(ctx); err != nil {
		return err
	}
	logger.Info("orientation platform stopped cleanly")
	return nil
}
