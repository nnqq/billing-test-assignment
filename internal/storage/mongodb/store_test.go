package mongodb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

// newTestStore talks to a real mongo because the summary arithmetic lives in an
// aggregation pipeline, and a fake would only test the fake.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("set MONGO_TEST_URI to run the storage integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	database := fmt.Sprintf("billing_test_%d", time.Now().UnixNano())

	store, err := Connect(ctx, uri, database)
	if err != nil {
		t.Fatalf("connect to %s: %v", uri, err)
	}

	err = store.EnsureIndexes(ctx)
	if err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()

		dropErr := store.transactions.Database().Drop(cleanupCtx)
		if dropErr != nil {
			t.Errorf("drop test database: %v", dropErr)
		}

		closeErr := store.Close(cleanupCtx)
		if closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	})

	return store
}

func at(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed.UTC()
}

func testRun() ImportRun {
	return ImportRun{Source: "seed.csv", IngestedAt: time.Now().UTC().Truncate(time.Millisecond)}
}

func seed(t *testing.T) []transaction.Transaction {
	t.Helper()

	return []transaction.Transaction{
		{ID: "T-1", MerchantID: "M-1", Timestamp: at(t, "2026-02-28T21:15:00Z"),
			AmountMinor: 31500, FeeMinor: 536, Currency: "SAR", MCC: "5411"},
		{ID: "T-2", MerchantID: "M-1", Timestamp: at(t, "2026-03-05T10:00:00Z"),
			AmountMinor: 100000, FeeMinor: 1700, Currency: "SAR", MCC: "5411"},
		{ID: "T-3", MerchantID: "M-1", Timestamp: at(t, "2026-03-06T10:00:00Z"),
			AmountMinor: -40000, FeeMinor: 0, Currency: "SAR", MCC: "5411"},
		{ID: "T-4", MerchantID: "M-1", Timestamp: at(t, "2026-03-07T10:00:00Z"),
			AmountMinor: 50000, FeeMinor: 950, Currency: "USD", MCC: "5411"},
		{ID: "T-5", MerchantID: "M-2", Timestamp: at(t, "2026-03-07T10:00:00Z"),
			AmountMinor: 70000, FeeMinor: 1400, Currency: "SAR", MCC: "5812"},
		{ID: "T-6", MerchantID: "M-1", Timestamp: at(t, "2026-03-31T21:30:00Z"),
			AmountMinor: 46000, FeeMinor: 782, Currency: "SAR", MCC: "5411"},
	}
}

func TestSummaryGroupsByCurrency(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	totals, err := store.Summary(ctx, transaction.Filter{MerchantID: "M-1"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if len(totals) != 2 {
		t.Fatalf("totals = %+v, want SAR and USD", totals)
	}

	sar := totals[0]
	if sar.Currency != "SAR" {
		t.Fatalf("first total = %q, want SAR (sorted)", sar.Currency)
	}
	want := transaction.CurrencyTotals{
		Currency:      "SAR",
		TurnoverMinor: 177500,
		RefundsMinor:  40000,
		NetMinor:      137500,
		FeeMinor:      3018,
		SaleCount:     3,
		RefundCount:   1,
	}
	if sar != want {
		t.Errorf("SAR totals = %+v, want %+v", sar, want)
	}

	usd := totals[1]
	if usd.Currency != "USD" || usd.TurnoverMinor != 50000 || usd.FeeMinor != 950 {
		t.Errorf("USD totals = %+v", usd)
	}
}

// The period is half open, so a row sitting exactly on each edge decides the
// contract: from is in, to is out.
func TestSummaryPeriodIsHalfOpen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	from := at(t, "2026-02-28T21:15:00Z")
	to := at(t, "2026-03-31T21:30:00Z")

	totals, err := store.Summary(ctx, transaction.Filter{
		MerchantID: "M-1",
		From:       &from,
		To:         &to,
		Currency:   "SAR",
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if len(totals) != 1 {
		t.Fatalf("totals = %+v, want one row", totals)
	}
	if totals[0].SaleCount != 2 {
		t.Errorf("sale_count = %d, want 2: the row on from is in, the row on to is out",
			totals[0].SaleCount)
	}
	if totals[0].TurnoverMinor != 131500 {
		t.Errorf("turnover_minor = %d, want 131500", totals[0].TurnoverMinor)
	}
	if totals[0].FeeMinor != 2236 {
		t.Errorf("fee_minor = %d, want 2236", totals[0].FeeMinor)
	}
}

func TestSaveTransactionsIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.Upserted != 6 || first.Replaced != 0 {
		t.Errorf("first save = %+v, want 6 upserted", first)
	}

	second, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Upserted != 0 || second.Replaced != 6 {
		t.Errorf("second save = %+v, want 6 replaced and nothing new", second)
	}

	count, err := store.transactions.CountDocuments(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 6 {
		t.Errorf("documents = %d, want 6 after importing the same feed twice", count)
	}

	totals, err := store.Summary(ctx, transaction.Filter{MerchantID: "M-1", Currency: "SAR"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if totals[0].FeeMinor != 3018 {
		t.Errorf("fee_minor = %d, want 3018: a repeat import must not double the fee",
			totals[0].FeeMinor)
	}
}

// A row whose source fee was blank still counts towards turnover, contributes
// zero fee, and is reported so the shortfall is visible.
func TestSummaryCountsRowsWithMissingFee(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rows := []transaction.Transaction{
		{ID: "T-A", MerchantID: "M-9", Timestamp: at(t, "2026-03-01T10:00:00Z"),
			AmountMinor: 283329, FeeMinor: 0, Currency: "SAR", MCC: "5411", FeeMissing: true},
		{ID: "T-B", MerchantID: "M-9", Timestamp: at(t, "2026-03-02T10:00:00Z"),
			AmountMinor: -283329, FeeMinor: 4817, Currency: "SAR", MCC: "5411"},
		{ID: "T-C", MerchantID: "M-9", Timestamp: at(t, "2026-03-03T10:00:00Z"),
			AmountMinor: 100000, FeeMinor: 1700, Currency: "SAR", MCC: "5411"},
	}

	_, err := store.SaveTransactions(ctx, rows, testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	totals, err := store.Summary(ctx, transaction.Filter{MerchantID: "M-9", Currency: "SAR"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("totals = %+v, want one row", totals)
	}

	got := totals[0]
	if got.FeeMissingCount != 1 {
		t.Errorf("fee_missing_count = %d, want 1", got.FeeMissingCount)
	}
	if got.TurnoverMinor != 383329 {
		t.Errorf("turnover_minor = %d, want 383329: the flagged sale still counts",
			got.TurnoverMinor)
	}
	if got.NetMinor != 100000 {
		t.Errorf("net_minor = %d, want 100000: sale and refund must cancel", got.NetMinor)
	}
	if got.FeeMinor != 6517 {
		t.Errorf("fee_minor = %d, want 6517", got.FeeMinor)
	}
}

func TestSummaryEmptyForUnknownPeriod(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	from := at(t, "2027-01-01T00:00:00Z")
	totals, err := store.Summary(ctx, transaction.Filter{MerchantID: "M-1", From: &from})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(totals) != 0 {
		t.Errorf("totals = %+v, want none", totals)
	}
}

func TestMerchantLookups(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	exists, err := store.MerchantExists(ctx, "M-1")
	if err != nil {
		t.Fatalf("merchant exists: %v", err)
	}
	if !exists {
		t.Error("M-1 must exist")
	}

	exists, err = store.MerchantExists(ctx, "M-9999")
	if err != nil {
		t.Fatalf("merchant exists: %v", err)
	}
	if exists {
		t.Error("M-9999 must not exist")
	}

	page, err := store.MerchantIDs(ctx, "", 100)
	if err != nil {
		t.Fatalf("merchant ids: %v", err)
	}
	if len(page.IDs) != 2 || page.IDs[0] != "M-1" || page.IDs[1] != "M-2" {
		t.Errorf("merchant ids = %v, want [M-1 M-2] sorted", page.IDs)
	}
	if page.NextAfter != "" {
		t.Errorf("next_after = %q, want empty: the listing fits in one page", page.NextAfter)
	}
}

// The listing has to page: a merchant book that outgrows one response must
// still be walkable, which distinct could not offer.
func TestMerchantIDsPaginate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	first, err := store.MerchantIDs(ctx, "", 1)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.IDs) != 1 || first.IDs[0] != "M-1" {
		t.Fatalf("first page = %+v, want [M-1]", first)
	}
	if first.NextAfter != "M-1" {
		t.Fatalf("next_after = %q, want M-1", first.NextAfter)
	}

	second, err := store.MerchantIDs(ctx, first.NextAfter, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.IDs) != 1 || second.IDs[0] != "M-2" {
		t.Fatalf("second page = %+v, want [M-2]", second)
	}
	if second.NextAfter != "" {
		t.Errorf("next_after = %q, want empty at the end of the listing", second.NextAfter)
	}
}

// A page of zero has no last element, and the cursor used to be read off one:
// the listing indexed IDs[-1] and panicked as soon as more rows existed than
// the page could hold.
func TestMerchantIDsRejectsNonPositiveLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.SaveTransactions(ctx, seed(t), testRun())
	if err != nil {
		t.Fatalf("save transactions: %v", err)
	}

	for _, limit := range []int{0, -1} {
		_, listErr := store.MerchantIDs(ctx, "", limit)
		if listErr == nil {
			t.Errorf("MerchantIDs with limit %d must fail instead of paging", limit)
		}
	}
}

// Every row has to say which load wrote it, and first_seen_at has to survive a
// re-import: source and ingested_at move, the moment the id first arrived does
// not.
func TestSaveTransactionsRecordsLineage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first := ImportRun{Source: "monday.csv", IngestedAt: at(t, "2026-04-01T10:00:00Z")}
	_, err := store.SaveTransactions(ctx, seed(t), first)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	type stored struct {
		Source      string    `bson:"source"`
		IngestedAt  time.Time `bson:"ingested_at"`
		FirstSeenAt time.Time `bson:"first_seen_at"`
	}

	var got stored
	filter := bson.D{{Key: "transaction_id", Value: "T-1"}}
	err = store.transactions.FindOne(ctx, filter).Decode(&got)
	if err != nil {
		t.Fatalf("find T-1: %v", err)
	}
	if got.Source != "monday.csv" {
		t.Errorf("source = %q, want monday.csv", got.Source)
	}
	if !got.IngestedAt.Equal(first.IngestedAt) || !got.FirstSeenAt.Equal(first.IngestedAt) {
		t.Errorf("stamps = %+v, want both at %s", got, first.IngestedAt)
	}

	second := ImportRun{Source: "tuesday.csv", IngestedAt: at(t, "2026-04-02T10:00:00Z")}
	_, err = store.SaveTransactions(ctx, seed(t), second)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}

	err = store.transactions.FindOne(ctx, filter).Decode(&got)
	if err != nil {
		t.Fatalf("find T-1 again: %v", err)
	}
	if got.Source != "tuesday.csv" || !got.IngestedAt.Equal(second.IngestedAt) {
		t.Errorf("after re-import = %+v, want the tuesday stamps", got)
	}
	if !got.FirstSeenAt.Equal(first.IngestedAt) {
		t.Errorf("first_seen_at = %s, want it pinned to the first load at %s",
			got.FirstSeenAt, first.IngestedAt)
	}
}
