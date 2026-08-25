package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

const csvPath = "../testdata/transactions.csv"

// The response shapes are declared here rather than imported from internal so
// the test pins the JSON contract itself: renaming a tag has to fail here.
type apiTotals struct {
	Currency        string `json:"currency"`
	TurnoverMinor   int64  `json:"turnover_minor"`
	RefundsMinor    int64  `json:"refunds_minor"`
	NetMinor        int64  `json:"net_minor"`
	FeeMinor        int64  `json:"fee_minor"`
	SaleCount       int64  `json:"sale_count"`
	RefundCount     int64  `json:"refund_count"`
	FeeMissingCount int64  `json:"fee_missing_count"`
}

type apiSummary struct {
	MerchantID string      `json:"merchant_id"`
	Totals     []apiTotals `json:"totals"`
}

type apiMerchants struct {
	MerchantIDs []string `json:"merchant_ids"`
	NextAfter   string   `json:"next_after"`
}

func baseURL(t *testing.T) string {
	t.Helper()

	value := os.Getenv("API_BASE_URL")
	if value == "" {
		t.Skip("set API_BASE_URL to run the end to end tests, " +
			"pointing at a service that imported testdata/transactions.csv")
	}
	return value
}

func loadCSV(t *testing.T) []record {
	t.Helper()

	records, err := readCSV(csvPath)
	if err != nil {
		t.Fatalf("read %s: %v", csvPath, err)
	}
	if len(records) == 0 {
		t.Fatalf("%s produced no records", csvPath)
	}
	return records
}

func get(t *testing.T, endpoint string, query url.Values, out any) {
	t.Helper()

	target := baseURL(t) + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	client := &http.Client{Timeout: 15 * time.Second}

	response, err := client.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", target, response.StatusCode)
	}

	err = json.NewDecoder(response.Body).Decode(out)
	if err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}
}

func summaryOf(t *testing.T, merchantID, currency string, period window) apiSummary {
	t.Helper()

	query := url.Values{}
	if currency != "" {
		query.Set("currency", currency)
	}
	if period.From != nil {
		query.Set("from", period.From.Format(time.RFC3339))
	}
	if period.To != nil {
		query.Set("to", period.To.Format(time.RFC3339))
	}

	var body apiSummary
	get(t, "/merchants/"+merchantID+"/summary", query, &body)
	return body
}

func compare(t *testing.T, label string, got apiTotals, want totals) {
	t.Helper()

	checks := []struct {
		field string
		got   int64
		want  int64
	}{
		{"turnover_minor", got.TurnoverMinor, want.TurnoverMinor},
		{"refunds_minor", got.RefundsMinor, want.RefundsMinor},
		{"net_minor", got.NetMinor, want.NetMinor},
		{"fee_minor", got.FeeMinor, want.FeeMinor},
		{"sale_count", got.SaleCount, want.SaleCount},
		{"refund_count", got.RefundCount, want.RefundCount},
		{"fee_missing_count", got.FeeMissingCount, want.FeeMissingCount},
	}

	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s: %s = %d, csv says %d (off by %d)",
				label, check.field, check.got, check.want, check.got-check.want)
		}
	}
}

// TestSummaryMatchesCSV walks every merchant and currency in the feed and
// compares the service against sums taken from the file itself.
func TestSummaryMatchesCSV(t *testing.T) {
	records := loadCSV(t)

	for _, merchantID := range merchantsOf(records) {
		for _, currency := range currenciesOf(records, merchantID) {
			name := fmt.Sprintf("%s/%s", merchantID, currency)

			t.Run(name, func(t *testing.T) {
				body := summaryOf(t, merchantID, currency, window{})

				if body.MerchantID != merchantID {
					t.Fatalf("merchant_id = %q, want %q", body.MerchantID, merchantID)
				}
				if len(body.Totals) != 1 {
					t.Fatalf("totals = %+v, want exactly one row for %s", body.Totals, currency)
				}
				compare(t, name, body.Totals[0], sumFor(records, merchantID, currency, window{}))
			})
		}
	}
}

// TestSummaryWithoutCurrencyMatchesCSV checks the unfiltered call, where the
// service has to split a merchant across every currency it traded in.
func TestSummaryWithoutCurrencyMatchesCSV(t *testing.T) {
	records := loadCSV(t)

	for _, merchantID := range merchantsOf(records) {
		t.Run(merchantID, func(t *testing.T) {
			body := summaryOf(t, merchantID, "", window{})

			wantCurrencies := currenciesOf(records, merchantID)
			if len(body.Totals) != len(wantCurrencies) {
				t.Fatalf("totals has %d rows, csv has %d currencies %v",
					len(body.Totals), len(wantCurrencies), wantCurrencies)
			}

			for _, row := range body.Totals {
				want := sumFor(records, merchantID, row.Currency, window{})
				compare(t, merchantID+"/"+row.Currency, row, want)
			}
		})
	}
}

// TestSummaryPeriodsMatchCSV pins the half open interval against the rows the
// feed puts within three hours of both edges of March.
func TestSummaryPeriodsMatchCSV(t *testing.T) {
	records := loadCSV(t)

	periods := []struct {
		name string
		from string
		to   string
	}{
		{"march in riyadh", "2026-03-01T00:00:00+03:00", "2026-04-01T00:00:00+03:00"},
		{"march in utc", "2026-03-01T00:00:00Z", "2026-04-01T00:00:00Z"},
		{"mid month slice", "2026-03-10T00:00:00Z", "2026-03-20T00:00:00Z"},
		{"open ended start", "", "2026-03-15T00:00:00Z"},
		{"open ended finish", "2026-03-15T00:00:00Z", ""},
	}

	for _, period := range periods {
		for _, merchantID := range merchantsOf(records) {
			name := fmt.Sprintf("%s/%s", period.name, merchantID)

			t.Run(name, func(t *testing.T) {
				bounds := window{From: parseBound(t, period.from), To: parseBound(t, period.to)}

				body := summaryOf(t, merchantID, "SAR", bounds)
				if len(body.Totals) != 1 {
					t.Fatalf("totals = %+v, want exactly one SAR row", body.Totals)
				}
				compare(t, name, body.Totals[0], sumFor(records, merchantID, "SAR", bounds))
			})
		}
	}
}

// TestRefundsHaveMatchingSales guards the defect that dropping a row over a
// blank fee once caused: a refund counted against a sale that was never booked.
func TestRefundsHaveMatchingSales(t *testing.T) {
	records := loadCSV(t)

	sales := make(map[string]bool)
	for _, item := range records {
		if item.AmountMinor > 0 {
			sales[fmt.Sprintf("%s|%s|%d", item.MerchantID, item.Currency, item.AmountMinor)] = true
		}
	}

	for _, item := range records {
		if item.AmountMinor >= 0 {
			continue
		}

		key := fmt.Sprintf("%s|%s|%d", item.MerchantID, item.Currency, -item.AmountMinor)
		if !sales[key] {
			t.Errorf("refund %s (%s %s %d) has no matching sale in the imported set",
				item.ID, item.MerchantID, item.Currency, item.AmountMinor)
		}
	}
}

func TestMerchantsMatchCSV(t *testing.T) {
	records := loadCSV(t)

	listed := listMerchants(t, 0)

	inAPI := make(map[string]bool, len(listed))
	for _, id := range listed {
		inAPI[id] = true
	}

	for _, merchantID := range merchantsOf(records) {
		if !inAPI[merchantID] {
			t.Errorf("merchant %s is in the csv but not in /merchants", merchantID)
		}
	}
	if len(listed) != len(merchantsOf(records)) {
		t.Errorf("/merchants returned %d ids, csv has %d", len(listed), len(merchantsOf(records)))
	}
}

// The listing pages, so walking it with a page size of one has to end up at the
// same set as asking for all of it at once.
func TestMerchantsPaginate(t *testing.T) {
	wholeList := listMerchants(t, 0)
	oneAtATime := listMerchants(t, 1)

	if !slices.Equal(wholeList, oneAtATime) {
		t.Errorf("paged listing = %v, single page = %v", oneAtATime, wholeList)
	}
}

// listMerchants follows next_after to the end. A limit of 0 leaves the page
// size to the service.
func listMerchants(t *testing.T, limit int) []string {
	t.Helper()

	var all []string
	after := ""

	for page := 0; ; page++ {
		if page > 100 {
			t.Fatalf("/merchants did not stop paging after %d pages", page)
		}

		query := url.Values{}
		if limit > 0 {
			query.Set("limit", strconv.Itoa(limit))
		}
		if after != "" {
			query.Set("after", after)
		}

		var body apiMerchants
		get(t, "/merchants", query, &body)
		all = append(all, body.MerchantIDs...)

		if body.NextAfter == "" {
			return all
		}
		after = body.NextAfter
	}
}

func parseBound(t *testing.T, value string) *time.Time {
	t.Helper()

	if value == "" {
		return nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse bound %q: %v", value, err)
	}
	return &parsed
}
