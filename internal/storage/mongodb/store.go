// Package mongodb persists transactions and answers merchant summaries.
package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

const (
	transactionsCollection = "transactions"
	rejectsCollection      = "import_rejects"
	writeBatchSize         = 1000
)

type Store struct {
	client       *mongo.Client
	transactions *mongo.Collection
	rejects      *mongo.Collection
}

// document mirrors transaction.Transaction on disk plus the lineage of the load
// that wrote it. It carries no _id field on purpose: mongo generates one on
// insert and keeps the existing one on update.
type document struct {
	TransactionID string    `bson:"transaction_id"`
	MerchantID    string    `bson:"merchant_id"`
	Timestamp     time.Time `bson:"ts"`
	AmountMinor   int64     `bson:"amount_minor"`
	FeeMinor      int64     `bson:"fee_minor"`
	Currency      string    `bson:"currency"`
	MCC           string    `bson:"mcc"`
	FeeMissing    bool      `bson:"fee_missing"`

	// Source and IngestedAt name the load that last wrote this row. Without
	// them a figure in the summary cannot be traced back to the file it came
	// from, which is the first question asked when a total looks wrong.
	Source     string    `bson:"source"`
	IngestedAt time.Time `bson:"ingested_at"`
}

// ImportRun identifies one pass over one source file.
type ImportRun struct {
	Source     string
	IngestedAt time.Time
}

// WriteStats separates rows the import created from rows it overwrote. Mongo
// counts a full rewrite as a match even when the content is identical, so a
// single summed counter could not tell a fresh load from a repeat run.
type WriteStats struct {
	Upserted int64
	Replaced int64
}

// RejectedRow is a source row that never became a transaction.
type RejectedRow struct {
	Source     string    `bson:"source"`
	Line       int       `bson:"line"`
	Row        []string  `bson:"row,omitempty"`
	Reason     string    `bson:"reason"`
	RejectedAt time.Time `bson:"rejected_at"`
}

// MerchantPage is one page of merchant ids. NextAfter is empty once the listing
// is exhausted, otherwise it is the cursor to pass back as `after`.
type MerchantPage struct {
	IDs       []string
	NextAfter string
}

func Connect(ctx context.Context, uri, database string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		disconnectErr := client.Disconnect(context.WithoutCancel(ctx))
		if disconnectErr != nil {
			return nil, fmt.Errorf("ping mongo: %w (disconnect: %v)", err, disconnectErr)
		}
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	db := client.Database(database)
	return &Store{
		client:       client,
		transactions: db.Collection(transactionsCollection),
		rejects:      db.Collection(rejectsCollection),
	}, nil
}

func (s *Store) Close(ctx context.Context) error {
	err := s.client.Disconnect(ctx)
	if err != nil {
		return fmt.Errorf("disconnect from mongo: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	err := s.client.Ping(ctx, nil)
	if err != nil {
		return fmt.Errorf("ping mongo: %w", err)
	}
	return nil
}

func (s *Store) EnsureIndexes(ctx context.Context) error {
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "transaction_id", Value: 1}},
			Options: options.Index().SetName("transaction_id_unique").SetUnique(true),
		},
		{
			Keys: bson.D{
				{Key: "merchant_id", Value: 1},
				{Key: "currency", Value: 1},
				{Key: "ts", Value: 1},
			},
			Options: options.Index().SetName("merchant_currency_ts"),
		},
	}

	_, err := s.transactions.Indexes().CreateMany(ctx, models)
	if err != nil {
		return fmt.Errorf("create transactions indexes: %w", err)
	}
	return nil
}

// SaveTransactions matches on transaction_id and upserts, which makes the
// import idempotent across restarts and collapses any duplicate the feed still
// carries. The unique index on transaction_id is what keeps that true when two
// importers race on a row that does not exist yet.
//
// The update is $set rather than a replace so that first_seen_at, written once
// by $setOnInsert, survives every later load: source and ingested_at say where
// the current content came from, first_seen_at says when the id was first seen.
func (s *Store) SaveTransactions(
	ctx context.Context,
	txs []transaction.Transaction,
	run ImportRun,
) (WriteStats, error) {
	var stats WriteStats

	for start := 0; start < len(txs); start += writeBatchSize {
		end := min(start+writeBatchSize, len(txs))

		models := make([]mongo.WriteModel, 0, end-start)
		for _, tx := range txs[start:end] {
			doc := document{
				TransactionID: tx.ID,
				MerchantID:    tx.MerchantID,
				Timestamp:     tx.Timestamp,
				AmountMinor:   tx.AmountMinor,
				FeeMinor:      tx.FeeMinor,
				Currency:      tx.Currency,
				MCC:           tx.MCC,
				FeeMissing:    tx.FeeMissing,
				Source:        run.Source,
				IngestedAt:    run.IngestedAt,
			}
			update := bson.D{
				{Key: "$set", Value: doc},
				{Key: "$setOnInsert", Value: bson.D{
					{Key: "first_seen_at", Value: run.IngestedAt},
				}},
			}
			models = append(models, mongo.NewUpdateOneModel().
				SetFilter(bson.D{{Key: "transaction_id", Value: tx.ID}}).
				SetUpdate(update).
				SetUpsert(true))
		}

		result, err := s.transactions.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
		if err != nil {
			return stats, fmt.Errorf("bulk write transactions at offset %d: %w", start, err)
		}
		stats.Upserted += result.UpsertedCount
		stats.Replaced += result.MatchedCount
	}

	return stats, nil
}

// ReplaceRejects swaps out everything previously recorded for this source, so
// re-running the import does not stack the same rejects over and over.
func (s *Store) ReplaceRejects(ctx context.Context, source string, rows []RejectedRow) error {
	_, err := s.rejects.DeleteMany(ctx, bson.D{{Key: "source", Value: source}})
	if err != nil {
		return fmt.Errorf("clear previous import rejects for %q: %w", source, err)
	}
	if len(rows) == 0 {
		return nil
	}

	docs := make([]any, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, row)
	}

	_, err = s.rejects.InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("insert import rejects: %w", err)
	}
	return nil
}

func (s *Store) MerchantExists(ctx context.Context, merchantID string) (bool, error) {
	filter := bson.D{{Key: "merchant_id", Value: merchantID}}

	count, err := s.transactions.CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("count documents for merchant %q: %w", merchantID, err)
	}
	return count > 0, nil
}

// MerchantIDs returns up to limit ids strictly greater than after, ascending.
// A limit below one has no page to describe, and the caller is the only one who
// can say what it meant, so it is an error rather than a silent default.
//
// It groups rather than calling distinct: a distinct result is one BSON
// document and stops working past 16MB, which on a real book of merchants is a
// hard ceiling with no way to page around it.
func (s *Store) MerchantIDs(
	ctx context.Context,
	after string,
	limit int,
) (MerchantPage, error) {
	if limit < 1 {
		return MerchantPage{}, fmt.Errorf("list merchant ids: limit must be positive, got %d", limit)
	}

	pipeline := mongo.Pipeline{}
	if after != "" {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.D{
			{Key: "merchant_id", Value: bson.D{{Key: "$gt", Value: after}}},
		}}})
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$merchant_id"}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
		// One past the page, so a full page can be told from the last one.
		bson.D{{Key: "$limit", Value: int64(limit) + 1}},
	)

	cursor, err := s.transactions.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return MerchantPage{}, fmt.Errorf("list merchant ids: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []struct {
		ID string `bson:"_id"`
	}
	err = cursor.All(ctx, &rows)
	if err != nil {
		return MerchantPage{}, fmt.Errorf("decode merchant ids: %w", err)
	}

	page := MerchantPage{IDs: make([]string, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		page.IDs = append(page.IDs, row.ID)
	}
	if len(rows) > limit {
		page.NextAfter = page.IDs[len(page.IDs)-1]
	}
	return page, nil
}
