package mongodb

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

type summaryRow struct {
	Currency      string `bson:"_id"`
	TurnoverMinor int64  `bson:"turnover_minor"`
	RefundsMinor  int64  `bson:"refunds_minor"`
	FeeMinor      int64  `bson:"fee_minor"`
	SaleCount     int64  `bson:"sale_count"`
	RefundCount   int64  `bson:"refund_count"`

	FeeMissingCount int64 `bson:"fee_missing_count"`
}

// Summary groups a merchant's rows per currency. The arithmetic runs inside
// mongo so the answer stays one round trip regardless of how many rows match.
func (s *Store) Summary(
	ctx context.Context,
	filter transaction.Filter,
) ([]transaction.CurrencyTotals, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: matchStage(filter)}},
		bson.D{{Key: "$group", Value: groupStage()}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := s.transactions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate summary for merchant %q: %w", filter.MerchantID, err)
	}
	defer cursor.Close(ctx)

	var rows []summaryRow
	err = cursor.All(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("decode summary for merchant %q: %w", filter.MerchantID, err)
	}

	totals := make([]transaction.CurrencyTotals, 0, len(rows))
	for _, row := range rows {
		totals = append(totals, transaction.CurrencyTotals{
			Currency:      row.Currency,
			TurnoverMinor: row.TurnoverMinor,
			RefundsMinor:  row.RefundsMinor,
			NetMinor:      row.TurnoverMinor - row.RefundsMinor,
			FeeMinor:      row.FeeMinor,
			SaleCount:     row.SaleCount,
			RefundCount:   row.RefundCount,

			FeeMissingCount: row.FeeMissingCount,
		})
	}
	return totals, nil
}

func matchStage(filter transaction.Filter) bson.D {
	stage := bson.D{{Key: "merchant_id", Value: filter.MerchantID}}

	if filter.Currency != "" {
		stage = append(stage, bson.E{Key: "currency", Value: filter.Currency})
	}

	period := bson.D{}
	if filter.From != nil {
		period = append(period, bson.E{Key: "$gte", Value: *filter.From})
	}
	if filter.To != nil {
		period = append(period, bson.E{Key: "$lt", Value: *filter.To})
	}
	if len(period) > 0 {
		stage = append(stage, bson.E{Key: "ts", Value: period})
	}

	return stage
}

func groupStage() bson.D {
	isRefund := bson.D{{Key: "$lt", Value: bson.A{"$amount_minor", 0}}}
	absAmount := bson.D{{Key: "$abs", Value: "$amount_minor"}}
	feeMissing := bson.D{{Key: "$eq", Value: bson.A{"$fee_missing", true}}}

	return bson.D{
		{Key: "_id", Value: "$currency"},
		{Key: "turnover_minor", Value: sumIf(isRefund, 0, "$amount_minor")},
		{Key: "refunds_minor", Value: sumIf(isRefund, absAmount, 0)},
		{Key: "fee_minor", Value: bson.D{{Key: "$sum", Value: "$fee_minor"}}},
		{Key: "sale_count", Value: sumIf(isRefund, 0, 1)},
		{Key: "refund_count", Value: sumIf(isRefund, 1, 0)},
		{Key: "fee_missing_count", Value: sumIf(feeMissing, 1, 0)},
	}
}

func sumIf(condition bson.D, whenTrue, whenFalse any) bson.D {
	return bson.D{{Key: "$sum", Value: bson.D{
		{Key: "$cond", Value: bson.A{condition, whenTrue, whenFalse}},
	}}}
}
