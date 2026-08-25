// Command importer loads one CSV feed into the transactions collection and
// exits. It is a separate binary from the API on purpose: a feed the parser
// cannot read is a failed load, not a reason for the read model to stop serving
// the data it already has, and one writer at a time lets the API scale out.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/config"
	"github.com/nnqq/billing-test-assignment/internal/importer"
	"github.com/nnqq/billing-test-assignment/internal/storage/mongodb"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	err := run(log)
	if err != nil {
		log.Error("import failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.CSVPath == "" {
		return errors.New("CSV_PATH must name the feed to import")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, cfg.ImportTimeout)
	defer cancel()

	store, err := mongodb.Connect(ctx, cfg.MongoURI, cfg.MongoDatabase)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer closeStore(store, cfg.ShutdownTimeout, log)

	err = store.EnsureIndexes(ctx)
	if err != nil {
		return fmt.Errorf("prepare storage: %w", err)
	}

	return importCSV(ctx, store, cfg.CSVPath, log)
}

func importCSV(ctx context.Context, store *mongodb.Store, path string, log *slog.Logger) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open csv %q: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	result, err := importer.ParseCSV(file)
	if err != nil {
		return fmt.Errorf("parse csv %q: %w", path, err)
	}

	run := mongodb.ImportRun{Source: path, IngestedAt: time.Now().UTC()}

	stats, err := store.SaveTransactions(ctx, result.Transactions, run)
	if err != nil {
		return fmt.Errorf("save transactions from %q: %w", path, err)
	}

	rows := make([]mongodb.RejectedRow, 0, len(result.Rejects))
	for _, reject := range result.Rejects {
		rows = append(rows, mongodb.RejectedRow{
			Source:     run.Source,
			Line:       reject.Line,
			Row:        reject.Row,
			Reason:     reject.Reason,
			RejectedAt: run.IngestedAt,
		})
	}

	err = store.ReplaceRejects(ctx, path, rows)
	if err != nil {
		return fmt.Errorf("record import rejects from %q: %w", path, err)
	}

	log.Info("csv import finished",
		slog.String("path", path),
		slog.Time("ingested_at", run.IngestedAt),
		slog.Int("rows_read", result.TotalRows),
		slog.Int("accepted", len(result.Transactions)),
		slog.Int("duplicates_skipped", result.Duplicates),
		slog.Int("rejected", len(result.Rejects)),
		slog.Int("fee_missing", result.FeeMissing),
		slog.Int64("inserted", stats.Upserted),
		slog.Int64("overwritten", stats.Replaced),
	)

	for _, reject := range result.Rejects {
		log.Warn("csv row rejected",
			slog.Int("line", reject.Line),
			slog.String("reason", reject.Reason),
		)
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
