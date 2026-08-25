package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/config"
	"github.com/nnqq/billing-test-assignment/internal/httpapi"
	"github.com/nnqq/billing-test-assignment/internal/storage/mongodb"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	err := run(log)
	if err != nil {
		log.Error("service stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

// run serves the read model and nothing else. Loading the feed lives in
// cmd/importer: with the import inline, a header change in the CSV took the API
// down on a database that was perfectly able to answer, and the single writer
// it implied is what pinned the deployment to one replica.
func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.CSVPath != "" {
		log.Warn("CSV_PATH is set but the api does not import; run cmd/importer instead",
			slog.String("path", cfg.CSVPath))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancelStartup()

	store, err := mongodb.Connect(startupCtx, cfg.MongoURI, cfg.MongoDatabase)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer closeStore(store, cfg.ShutdownTimeout, log)

	err = store.EnsureIndexes(startupCtx)
	if err != nil {
		return fmt.Errorf("prepare storage: %w", err)
	}

	cancelStartup()
	return serve(ctx, cfg, store, log)
}

func serve(ctx context.Context, cfg config.Config, store *mongodb.Store, log *slog.Logger) error {
	api := httpapi.New(store, log, cfg.RequestTimeout)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	listenFailed := make(chan error, 1)
	go func() {
		listenErr := server.ListenAndServe()
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			listenFailed <- listenErr
			return
		}
		close(listenFailed)
	}()

	log.Info("http server listening", slog.String("addr", cfg.HTTPAddr))

	select {
	case listenErr := <-listenFailed:
		if listenErr != nil {
			return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, listenErr)
		}
		return nil
	case <-ctx.Done():
	}

	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	err := server.Shutdown(shutdownCtx)
	if err != nil {
		return fmt.Errorf("shut down http server: %w", err)
	}
	return nil
}

func closeStore(store *mongodb.Store, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := store.Close(ctx)
	if err != nil {
		log.Error("close storage", slog.Any("error", err))
	}
}
