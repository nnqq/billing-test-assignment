package importer

import (
	"os"
	"strings"
	"testing"
	"time"
)

const feed = "transaction_id,merchant_id,timestamp,amount,currency,mcc,fee\r\n" +
	"T-1,M-1,2026-02-28T21:15:00Z,315.00,SAR,5411,5.3550\r\n" +
	"T-2,M-1,2026-03-01T00:28:22+03:00,1608.17,sar,5541,24.1226\r\n" +
	"T-3,M-2,2026-03-01T02:58:13Z,2740.87,SAR,0742,54.8174\r\n" +
	"T-4,M-1,2026-03-04T23:21:18Z,-2597.77,SAR,5411,0.0000\r\n" +
	"T-3,M-2,2026-03-01T05:58:13+03:00,2740.87,SAR,0742,54.8174\r\n" +
	"T-5,M-1,2026-03-05T17:14:12+03:00,2833.29,SAR,5411,\r\n" +
	"T-6,M-1,2026-03-05T17:14:12+03:00,100.00,SAR,5411,-1.00\r\n" +
	"T-7,M-1,not-a-date,100.00,SAR,5411,1.00\r\n" +
	"T-3,M-2,2026-03-01T02:58:13Z,999.99,SAR,0742,54.8174\r\n"

func TestParseCSV(t *testing.T) {
	result, err := ParseCSV(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	if result.TotalRows != 9 {
		t.Errorf("TotalRows = %d, want 9", result.TotalRows)
	}
	if len(result.Transactions) != 5 {
		t.Fatalf("Transactions = %d, want 5", len(result.Transactions))
	}
	if result.Duplicates != 1 {
		t.Errorf("Duplicates = %d, want 1", result.Duplicates)
	}
	if len(result.Rejects) != 3 {
		t.Errorf("Rejects = %d, want 3: %+v", len(result.Rejects), result.Rejects)
	}
	if result.FeeMissing != 1 {
		t.Errorf("FeeMissing = %d, want 1", result.FeeMissing)
	}

	first := result.Transactions[0]
	if first.AmountMinor != 31500 || first.FeeMinor != 536 {
		t.Errorf("T-1 = (%d, %d), want (31500, 536)", first.AmountMinor, first.FeeMinor)
	}
	if !first.Timestamp.Equal(time.Date(2026, 2, 28, 21, 15, 0, 0, time.UTC)) {
		t.Errorf("T-1 timestamp = %s, want 2026-02-28T21:15:00Z", first.Timestamp)
	}

	// +03:00 must be carried to UTC, not truncated to the wall clock.
	second := result.Transactions[1]
	if !second.Timestamp.Equal(time.Date(2026, 2, 28, 21, 28, 22, 0, time.UTC)) {
		t.Errorf("T-2 timestamp = %s, want 2026-02-28T21:28:22Z", second.Timestamp)
	}
	if second.Currency != "SAR" {
		t.Errorf("T-2 currency = %q, want SAR", second.Currency)
	}

	third := result.Transactions[2]
	if third.MCC != "0742" {
		t.Errorf("T-3 mcc = %q, want 0742 (leading zero preserved)", third.MCC)
	}

	fourth := result.Transactions[3]
	if fourth.AmountMinor != -259777 {
		t.Errorf("T-4 = %d, want a -259777 refund", fourth.AmountMinor)
	}
}

func TestParseCSVRejectReasons(t *testing.T) {
	result, err := ParseCSV(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	byLine := make(map[int]string, len(result.Rejects))
	for _, reject := range result.Rejects {
		byLine[reject.Line] = reject.Reason
	}

	cases := []struct {
		line     int
		contains string
	}{
		{8, "fee is negative"},
		{9, "timestamp"},
		{10, "conflicting data"},
	}

	_, blankFeeRejected := byLine[7]
	if blankFeeRejected {
		t.Error("line 7 has a blank fee: it must be imported and flagged, not rejected")
	}

	for _, c := range cases {
		reason, ok := byLine[c.line]
		if !ok {
			t.Errorf("line %d: expected a reject, got none", c.line)
			continue
		}
		if !strings.Contains(reason, c.contains) {
			t.Errorf("line %d reason = %q, want it to mention %q", c.line, reason, c.contains)
		}
	}
}

// T-20074 is refunded by T-20103. Dropping the sale while keeping the refund
// would understate net turnover by the full amount, so the blank fee must not
// cost us the row.
func TestParseCSVKeepsBlankFeeRowPairedWithRefund(t *testing.T) {
	file, err := os.Open("../../testdata/transactions.csv")
	if err != nil {
		t.Skipf("sample feed not available: %v", err)
	}
	defer file.Close()

	result, err := ParseCSV(file)
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	byID := make(map[string]int64, len(result.Transactions))
	flagged := make(map[string]bool, len(result.Transactions))
	for _, tx := range result.Transactions {
		byID[tx.ID] = tx.AmountMinor
		flagged[tx.ID] = tx.FeeMissing
	}

	sale, ok := byID["T-20074"]
	if !ok {
		t.Fatal("T-20074 must be imported despite its blank fee")
	}
	if !flagged["T-20074"] {
		t.Error("T-20074 must be flagged as fee missing")
	}

	refund, ok := byID["T-20103"]
	if !ok {
		t.Fatal("T-20103 must be imported")
	}
	if sale+refund != 0 {
		t.Errorf("sale %d and refund %d must cancel out", sale, refund)
	}
}

func TestParseCSVRejectsMalformedFee(t *testing.T) {
	feed := "transaction_id,merchant_id,timestamp,amount,currency,mcc,fee\r\n" +
		"T-1,M-1,2026-03-01T00:00:00Z,100.00,SAR,5411,abc\r\n" +
		"T-2,M-1,2026-03-01T00:00:00Z,100.00,SAR,5411,\r\n"

	result, err := ParseCSV(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	if len(result.Rejects) != 1 {
		t.Fatalf("Rejects = %+v, want only the malformed fee", result.Rejects)
	}
	if len(result.Transactions) != 1 || !result.Transactions[0].FeeMissing {
		t.Errorf("blank fee must survive as a flagged row, got %+v", result.Transactions)
	}
}

// A header may place the required columns anywhere. A row shorter than the
// widest of those positions used to index out of range and panic, which in the
// service meant a crash loop on startup rather than a reject.
func TestParseCSVShortRowAgainstWideHeader(t *testing.T) {
	feed := "x1,x2,x3,transaction_id,merchant_id,timestamp,amount,currency,mcc,fee\r\n" +
		"a,b,c,T-1,M-1,2026-03-01T00:00:00Z,100.00,SAR,5411,1.70\r\n" +
		"a,b,c,T-2,M-1,2026-03-01T00:00:00Z,100.00,SAR\r\n"

	result, err := ParseCSV(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	if len(result.Transactions) != 1 {
		t.Errorf("Transactions = %d, want 1", len(result.Transactions))
	}
	if len(result.Rejects) != 1 {
		t.Fatalf("Rejects = %+v, want the short row", result.Rejects)
	}
	if !strings.Contains(result.Rejects[0].Reason, "at least 10 columns") {
		t.Errorf("reason = %q, want it to name the required width", result.Rejects[0].Reason)
	}

	// The columns still have to be read by name, not by position.
	if result.Transactions[0].ID != "T-1" || result.Transactions[0].FeeMinor != 170 {
		t.Errorf("row = %+v, want T-1 with fee 170", result.Transactions[0])
	}
}

func TestParseCSVMissingColumns(t *testing.T) {
	_, err := ParseCSV(strings.NewReader("transaction_id,merchant_id\r\nT-1,M-1\r\n"))
	if err == nil {
		t.Fatal("expected an error for a header without the money columns")
	}
	if !strings.Contains(err.Error(), "missing columns") {
		t.Errorf("error = %v, want it to name the missing columns", err)
	}
}

func TestParseCSVRealFeed(t *testing.T) {
	file, err := os.Open("../../testdata/transactions.csv")
	if err != nil {
		t.Skipf("sample feed not available: %v", err)
	}
	defer file.Close()

	result, err := ParseCSV(file)
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	if result.TotalRows != 493 {
		t.Errorf("TotalRows = %d, want 493", result.TotalRows)
	}
	if result.Duplicates != 4 {
		t.Errorf("Duplicates = %d, want 4", result.Duplicates)
	}
	if len(result.Rejects) != 0 {
		t.Errorf("Rejects = %d, want 0: %+v", len(result.Rejects), result.Rejects)
	}
	if result.FeeMissing != 4 {
		t.Errorf("FeeMissing = %d, want 4", result.FeeMissing)
	}
	if len(result.Transactions) != 489 {
		t.Errorf("Transactions = %d, want 489", len(result.Transactions))
	}
}

// An unrecognised currency code is not a rounding detail: the scale of a major
// unit is unknown, so the amount cannot be normalised at all. Guessing two
// digits would book a JPY row a hundred times over.
func TestParseCSVRejectsUnknownCurrency(t *testing.T) {
	feed := "transaction_id,merchant_id,timestamp,amount,currency,mcc,fee\r\n" +
		"T-1,M-1,2026-03-01T00:00:00Z,100.00,SAR,5411,1.70\r\n" +
		"T-2,M-1,2026-03-01T00:00:00Z,100.00,SAT,5411,1.70\r\n" +
		"T-3,M-1,2026-03-01T00:00:00Z,1500,JPY,5411,25\r\n"

	result, err := ParseCSV(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	if len(result.Rejects) != 1 {
		t.Fatalf("Rejects = %+v, want only the SAT row", result.Rejects)
	}
	if !strings.Contains(result.Rejects[0].Reason, "unknown currency") {
		t.Errorf("reason = %q, want it to name the unknown currency", result.Rejects[0].Reason)
	}

	if len(result.Transactions) != 2 {
		t.Fatalf("Transactions = %+v, want SAR and JPY", result.Transactions)
	}

	// JPY has no fraction digits, so 1500 yen is 1500 minor units, not 150000.
	yen := result.Transactions[1]
	if yen.Currency != "JPY" || yen.AmountMinor != 1500 || yen.FeeMinor != 25 {
		t.Errorf("JPY row = %+v, want amount 1500 and fee 25 in minor units", yen)
	}
}
