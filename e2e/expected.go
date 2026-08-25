// Package e2e checks the running service against sums computed straight from
// the CSV. The arithmetic here deliberately avoids internal/money and uses
// math/big instead: reusing the production parser would let the same bug pass
// on both sides and the test would prove nothing.
package e2e

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"time"
)

type totals struct {
	TurnoverMinor   int64
	RefundsMinor    int64
	NetMinor        int64
	FeeMinor        int64
	SaleCount       int64
	RefundCount     int64
	FeeMissingCount int64
}

type record struct {
	ID          string
	MerchantID  string
	Timestamp   time.Time
	AmountMinor int64
	FeeMinor    int64
	Currency    string
	FeeMissing  bool
}

type window struct {
	From *time.Time
	To   *time.Time
}

// readCSV applies the same intake rules as the importer: first row wins for a
// repeated id, and a blank fee becomes zero rather than dropping the row.
func readCSV(path string) ([]record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header of %q: %w", path, err)
	}

	column := make(map[string]int, len(header))
	for i, name := range header {
		column[strings.ToLower(strings.TrimSpace(name))] = i
	}

	var records []record
	seen := make(map[string]bool)
	line := 1

	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		line++
		if readErr != nil {
			return nil, fmt.Errorf("read %q line %d: %w", path, line, readErr)
		}

		id := strings.TrimSpace(row[column["transaction_id"]])
		if seen[id] {
			continue
		}
		seen[id] = true

		parsed, parseErr := toRecord(row, column)
		if parseErr != nil {
			return nil, fmt.Errorf("%q line %d: %w", path, line, parseErr)
		}
		records = append(records, parsed)
	}

	return records, nil
}

func toRecord(row []string, column map[string]int) (record, error) {
	field := func(name string) string {
		return strings.TrimSpace(row[column[name]])
	}

	timestamp, err := time.Parse(time.RFC3339, field("timestamp"))
	if err != nil {
		return record{}, fmt.Errorf("timestamp: %w", err)
	}

	currency := strings.ToUpper(field("currency"))
	scale, ok := fractionDigits[currency]
	if !ok {
		return record{}, fmt.Errorf("currency %q has no known scale", currency)
	}

	amountMinor, err := toMinorUnits(field("amount"), scale)
	if err != nil {
		return record{}, fmt.Errorf("amount: %w", err)
	}

	rawFee := field("fee")
	feeMissing := rawFee == ""

	var feeMinor int64
	if !feeMissing {
		feeMinor, err = toMinorUnits(rawFee, scale)
		if err != nil {
			return record{}, fmt.Errorf("fee: %w", err)
		}
	}

	return record{
		ID:          field("transaction_id"),
		MerchantID:  field("merchant_id"),
		Timestamp:   timestamp.UTC(),
		AmountMinor: amountMinor,
		FeeMinor:    feeMinor,
		Currency:    currency,
		FeeMissing:  feeMissing,
	}, nil
}

// fractionDigits is written out here rather than read from internal/money for
// the same reason the arithmetic below is: an oracle that shares a table with
// the code under test cannot catch a wrong entry in that table.
var fractionDigits = map[string]int{"SAR": 2, "USD": 2, "EUR": 2}

// toMinorUnits shifts an exact rational by the currency scale and rounds half
// away from zero. big.Rat holds "5.3550" without loss, so the tie at 535.5 is
// decided here rather than inherited from a float.
func toMinorUnits(decimal string, scale int) (int64, error) {
	value, ok := new(big.Rat).SetString(decimal)
	if !ok {
		return 0, fmt.Errorf("parse %q as a decimal", decimal)
	}

	shift := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	value.Mul(value, new(big.Rat).SetInt(shift))

	numerator := value.Num()
	denominator := value.Denom()

	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))

	twiceRemainder := new(big.Int).Abs(remainder)
	twiceRemainder.Lsh(twiceRemainder, 1)

	if twiceRemainder.Cmp(denominator) >= 0 {
		if numerator.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}

	if !quotient.IsInt64() {
		return 0, fmt.Errorf("value %q does not fit in int64 minor units", decimal)
	}
	return quotient.Int64(), nil
}

func sumFor(records []record, merchantID, currency string, period window) totals {
	var result totals

	for _, item := range records {
		if item.MerchantID != merchantID {
			continue
		}
		if currency != "" && item.Currency != currency {
			continue
		}
		if period.From != nil && item.Timestamp.Before(*period.From) {
			continue
		}
		if period.To != nil && !item.Timestamp.Before(*period.To) {
			continue
		}

		result.FeeMinor += item.FeeMinor
		if item.FeeMissing {
			result.FeeMissingCount++
		}

		if item.AmountMinor < 0 {
			result.RefundsMinor += -item.AmountMinor
			result.RefundCount++
			continue
		}
		result.TurnoverMinor += item.AmountMinor
		result.SaleCount++
	}

	result.NetMinor = result.TurnoverMinor - result.RefundsMinor
	return result
}

func currenciesOf(records []record, merchantID string) []string {
	seen := make(map[string]bool)
	var list []string

	for _, item := range records {
		if item.MerchantID != merchantID || seen[item.Currency] {
			continue
		}
		seen[item.Currency] = true
		list = append(list, item.Currency)
	}
	return list
}

func merchantsOf(records []record) []string {
	seen := make(map[string]bool)
	var list []string

	for _, item := range records {
		if seen[item.MerchantID] {
			continue
		}
		seen[item.MerchantID] = true
		list = append(list, item.MerchantID)
	}
	return list
}
