// Package transaction holds the billing domain model.
package transaction

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrEmptyID         = errors.New("transaction id is empty")
	ErrEmptyMerchantID = errors.New("merchant id is empty")
	ErrZeroTimestamp   = errors.New("timestamp is zero")
	ErrEmptyCurrency   = errors.New("currency is empty")
	ErrNegativeFee     = errors.New("fee is negative")

	ErrFeeMissingNotZero = errors.New("fee is marked missing but is not zero")
)

// Transaction is one normalised row of the source feed. Both money fields are
// minor units of Currency.
type Transaction struct {
	ID          string
	MerchantID  string
	Timestamp   time.Time
	AmountMinor int64
	FeeMinor    int64
	Currency    string
	MCC         string

	// FeeMissing marks a row whose source fee was blank. The row still counts
	// towards turnover: dropping it would strand the refund that cancels it.
	FeeMissing bool
}

func (t Transaction) Validate() error {
	if t.ID == "" {
		return ErrEmptyID
	}
	if t.MerchantID == "" {
		return ErrEmptyMerchantID
	}
	if t.Timestamp.IsZero() {
		return ErrZeroTimestamp
	}
	if t.Currency == "" {
		return ErrEmptyCurrency
	}
	if t.FeeMinor < 0 {
		return fmt.Errorf("%w: %d", ErrNegativeFee, t.FeeMinor)
	}
	if t.FeeMissing && t.FeeMinor != 0 {
		return fmt.Errorf("%w: %d", ErrFeeMissingNotZero, t.FeeMinor)
	}
	return nil
}

// Filter selects the rows a summary covers. From is inclusive, To is exclusive,
// both are optional, and an empty Currency means every currency.
type Filter struct {
	MerchantID string
	From       *time.Time
	To         *time.Time
	Currency   string
}

// CurrencyTotals aggregates one merchant's rows in a single currency. Sales and
// refunds stay apart: netting them would hide how much was charged back.
// RefundsMinor is a positive magnitude and FeeMinor is never negative.
type CurrencyTotals struct {
	Currency      string
	TurnoverMinor int64
	RefundsMinor  int64
	NetMinor      int64
	FeeMinor      int64
	SaleCount     int64
	RefundCount   int64

	// FeeMissingCount surfaces how much of FeeMinor is understated: every row
	// it counts contributed zero fee because the source left it blank.
	FeeMissingCount int64
}

// Equal compares two rows by value. Timestamps need Equal rather than ==,
// which also weighs the monotonic reading and the location pointer.
func (t Transaction) Equal(other Transaction) bool {
	return t.ID == other.ID &&
		t.MerchantID == other.MerchantID &&
		t.Timestamp.Equal(other.Timestamp) &&
		t.AmountMinor == other.AmountMinor &&
		t.FeeMinor == other.FeeMinor &&
		t.Currency == other.Currency &&
		t.MCC == other.MCC &&
		t.FeeMissing == other.FeeMissing
}
