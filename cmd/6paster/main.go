package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HextetSystems/6paster-go/internal/config"
	"github.com/HextetSystems/6paster-go/internal/storage"
	"github.com/HextetSystems/6paster-go/internal/web"
)

const usage = `Usage:
  6paster serve --config config.yaml
  6paster migrate --config config.yaml`

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("6paster stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "config.yaml", "path to YAML configuration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v\n%s", fs.Args(), usage)
	}
	if args[0] != "serve" && args[0] != "migrate" {
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	db, dialect, err := storage.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	store := storage.New(db, dialect)

	switch args[0] {
	case "migrate":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := store.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
		slog.Info("database migrations complete", "driver", cfg.Database.Driver)
		return nil
	case "serve":
		return serve(cfg, store)
	}
	return nil
}

func serve(cfg config.Config, store *storage.Store) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := store.CheckSchema(ctx); err != nil {
		return fmt.Errorf("database is not migrated (run `6paster migrate --config ...`): %w", err)
	}
	if err := store.Cleanup(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("initial cleanup: %w", err)
	}

	handler, err := web.New(cfg, store, slog.Default())
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go cleanupLoop(ctx, store, cfg.Cleanup.Interval.Duration)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "address", cfg.Server.Listen, "base_path", cfg.Server.BasePath)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func cleanupLoop(ctx context.Context, store *storage.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, time.Minute)
			err := store.Cleanup(cleanupCtx, now.UTC())
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("database cleanup failed", "error", err)
			}
		}
	}
}
