// Package importer turns the raw CSV feed into validated domain rows.
package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/money"
	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

const (
	colTransactionID = "transaction_id"
	colMerchantID    = "merchant_id"
	colTimestamp     = "timestamp"
	colAmount        = "amount"
	colCurrency      = "currency"
	colMCC           = "mcc"
	colFee           = "fee"
)

var requiredColumns = []string{
	colTransactionID, colMerchantID, colTimestamp,
	colAmount, colCurrency, colMCC, colFee,
}

// Reject is a source row that did not survive normalisation. Keeping it lets
// the operator see what was dropped instead of guessing from a total.
type Reject struct {
	Line   int
	Row    []string
	Reason string
}

type Result struct {
	Transactions []transaction.Transaction
	Rejects      []Reject
	TotalRows    int
	Duplicates   int
	FeeMissing   int
}

// ParseCSV reads the whole feed, normalising money into minor units and every
// timestamp into UTC. Rows repeating an id with identical content are dropped
// as duplicates; rows repeating an id with different content are rejected.
func ParseCSV(r io.Reader) (Result, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return Result{}, fmt.Errorf("read csv header: %w", err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}

	index, err := columnIndex(header)
	if err != nil {
		return Result{}, err
	}
	width := requiredWidth(index)

	var result Result
	seen := make(map[string]transaction.Transaction)
	line := 1

	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		line++
		if readErr != nil {
			var parseErr *csv.ParseError
			if !errors.As(readErr, &parseErr) {
				return Result{}, fmt.Errorf("read csv line %d: %w", line, readErr)
			}
			result.TotalRows++
			result.Rejects = append(result.Rejects, Reject{
				Line:   line,
				Reason: fmt.Sprintf("malformed csv row: %v", readErr),
			})
			continue
		}

		result.TotalRows++

		tx, rowErr := rowToTransaction(row, index, width)
		if rowErr != nil {
			result.Rejects = append(result.Rejects, Reject{
				Line:   line,
				Row:    row,
				Reason: rowErr.Error(),
			})
			continue
		}

		previous, isSeen := seen[tx.ID]
		if isSeen {
			if previous.Equal(tx) {
				result.Duplicates++
				continue
			}
			result.Rejects = append(result.Rejects, Reject{
				Line:   line,
				Row:    row,
				Reason: fmt.Sprintf("duplicate id %q with conflicting data", tx.ID),
			})
			continue
		}

		seen[tx.ID] = tx
		result.Transactions = append(result.Transactions, tx)
		if tx.FeeMissing {
			result.FeeMissing++
		}
	}

	return result, nil
}

func rowToTransaction(
	row []string,
	index map[string]int,
	width int,
) (transaction.Transaction, error) {
	// Guard against the widest column the header actually places, not against
	// the count of required columns: a header carrying extra leading columns
	// pushes those positions past 7, and a short row would index out of range.
	if len(row) < width {
		return transaction.Transaction{}, fmt.Errorf("expected at least %d columns, got %d",
			width, len(row))
	}

	field := func(name string) string {
		return strings.TrimSpace(row[index[name]])
	}

	ts, err := time.Parse(time.RFC3339, field(colTimestamp))
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("timestamp %q: %w", field(colTimestamp), err)
	}

	// The currency has to be resolved before any money is: it decides how many
	// fraction digits a major unit has, and an unrecognised code would have to
	// be guessed at. Guessing two digits is exactly how a JPY row turns into a
	// hundredfold overstatement, so an unknown code rejects the row instead.
	currency := strings.ToUpper(field(colCurrency))
	_, err = money.Exponent(currency)
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("currency: %w", err)
	}

	amountMinor, err := money.ParseMinor(field(colAmount), currency)
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("amount: %w", err)
	}

	// A blank fee is a hole in one field, not a broken row: the amount is still
	// valid and may be the counterpart of a refund elsewhere in the feed.
	// Anything else unparseable stays a reject.
	feeMinor, feeErr := money.ParseMinor(field(colFee), currency)
	feeMissing := errors.Is(feeErr, money.ErrEmpty)
	if feeErr != nil && !feeMissing {
		return transaction.Transaction{}, fmt.Errorf("fee: %w", feeErr)
	}

	tx := transaction.Transaction{
		ID:          field(colTransactionID),
		MerchantID:  field(colMerchantID),
		Timestamp:   ts.UTC(),
		AmountMinor: amountMinor,
		FeeMinor:    feeMinor,
		Currency:    currency,
		MCC:         field(colMCC),
		FeeMissing:  feeMissing,
	}

	err = tx.Validate()
	if err != nil {
		return transaction.Transaction{}, err
	}
	return tx, nil
}

func columnIndex(header []string) (map[string]int, error) {
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.ToLower(strings.TrimSpace(name))] = i
	}

	var missing []string
	for _, name := range requiredColumns {
		_, ok := index[name]
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("csv header is missing columns: %s", strings.Join(missing, ", "))
	}
	return index, nil
}

// requiredWidth is how many fields a row must have for every required column to
// be addressable.
func requiredWidth(index map[string]int) int {
	width := 0
	for _, name := range requiredColumns {
		position := index[name] + 1
		if position > width {
			width = position
		}
	}
	return width
}
