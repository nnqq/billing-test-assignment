package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/money"
	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

type periodResponse struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

// totalsResponse reports money in minor units, as integers. Sales and refunds
// stay in separate fields; fee_minor is never negative.
type totalsResponse struct {
	Currency      string `json:"currency"`
	TurnoverMinor int64  `json:"turnover_minor"`
	RefundsMinor  int64  `json:"refunds_minor"`
	NetMinor      int64  `json:"net_minor"`
	FeeMinor      int64  `json:"fee_minor"`
	SaleCount     int64  `json:"sale_count"`
	RefundCount   int64  `json:"refund_count"`

	// FeeMissingCount is how many rows in this bucket carried no fee in the
	// source. Non-zero means fee_minor is understated by that many rows.
	FeeMissingCount int64 `json:"fee_missing_count"`
}

type summaryResponse struct {
	MerchantID string           `json:"merchant_id"`
	Period     periodResponse   `json:"period"`
	Totals     []totalsResponse `json:"totals"`
}

type paramError struct {
	code    string
	message string
}

func (e *paramError) Error() string {
	return e.message
}

func (a *API) handleSummary(w http.ResponseWriter, r *http.Request) {
	merchantID := strings.TrimSpace(r.PathValue("merchant_id"))
	if merchantID == "" {
		a.writeError(w, r, http.StatusBadRequest, "invalid_merchant_id",
			"merchant id must not be empty", nil)
		return
	}

	filter, paramErr := parseFilter(merchantID, r.URL.Query())
	if paramErr != nil {
		a.writeError(w, r, http.StatusBadRequest, paramErr.code, paramErr.message, nil)
		return
	}

	totals, err := a.store.Summary(r.Context(), filter)
	if err != nil {
		a.writeStoreError(w, r, "could not build the summary", err)
		return
	}

	// An answer with rows proves the merchant exists, so the common case costs
	// one round trip. Only an empty result is ambiguous, and only then is a
	// second lookup worth making: it separates "no activity in this period"
	// from "no such merchant". There is no merchants collection, so existence
	// here means nothing more than having at least one transaction on file.
	if len(totals) == 0 {
		exists, existsErr := a.store.MerchantExists(r.Context(), merchantID)
		if existsErr != nil {
			a.writeStoreError(w, r, "could not look up the merchant", existsErr)
			return
		}
		if !exists {
			a.writeError(w, r, http.StatusNotFound, "merchant_not_found",
				fmt.Sprintf("merchant %q has no transactions", merchantID), nil)
			return
		}
	}

	a.writeJSON(w, r, http.StatusOK, buildResponse(filter, totals))
}

func buildResponse(
	filter transaction.Filter,
	totals []transaction.CurrencyTotals,
) summaryResponse {
	body := summaryResponse{
		MerchantID: filter.MerchantID,
		Period:     periodResponse{From: filter.From, To: filter.To},
		Totals:     make([]totalsResponse, 0, len(totals)),
	}

	// A caller that named a currency gets a row for it either way, so reading
	// fee_minor never has to be guarded by a length check first.
	if len(totals) == 0 && filter.Currency != "" {
		body.Totals = append(body.Totals, totalsResponse{Currency: filter.Currency})
		return body
	}

	for _, total := range totals {
		body.Totals = append(body.Totals, totalsResponse{
			Currency:      total.Currency,
			TurnoverMinor: total.TurnoverMinor,
			RefundsMinor:  total.RefundsMinor,
			NetMinor:      total.NetMinor,
			FeeMinor:      total.FeeMinor,
			SaleCount:     total.SaleCount,
			RefundCount:   total.RefundCount,

			FeeMissingCount: total.FeeMissingCount,
		})
	}
	return body
}

// parseFilter reads the period as a half-open interval: from is inclusive, to
// is exclusive, so adjacent months neither overlap nor leave a gap.
func parseFilter(merchantID string, query url.Values) (transaction.Filter, *paramError) {
	from, fromErr := parseTimeParam(query, "from")
	if fromErr != nil {
		return transaction.Filter{}, fromErr
	}

	to, toErr := parseTimeParam(query, "to")
	if toErr != nil {
		return transaction.Filter{}, toErr
	}

	if from != nil && to != nil && !from.Before(*to) {
		return transaction.Filter{}, &paramError{
			code:    "invalid_period",
			message: "from must be strictly before to",
		}
	}

	currency, currencyErr := parseCurrencyParam(query)
	if currencyErr != nil {
		return transaction.Filter{}, currencyErr
	}

	return transaction.Filter{
		MerchantID: merchantID,
		From:       from,
		To:         to,
		Currency:   currency,
	}, nil
}

// parseTimeParam insists on an explicit offset. A bare date would silently pick
// a timezone, and this feed has rows sitting within three hours of both edges
// of the month.
func parseTimeParam(query url.Values, name string) (*time.Time, *paramError) {
	raw := strings.TrimSpace(query.Get(name))
	if raw == "" {
		return nil, nil
	}

	value, parseErr := time.Parse(time.RFC3339, raw)
	if parseErr != nil {
		return nil, &paramError{code: "invalid_period", message: timeParamHint(name, raw)}
	}

	utc := value.UTC()
	return &utc, nil
}

// timeParamHint names the encoded offset, never a bare "+03:00": a plus in a
// query string decodes to a space, so the example the caller was told to copy
// would fail exactly the same way the value that got them here did. A space is
// the fingerprint of that mistake and is worth diagnosing by name.
func timeParamHint(name, raw string) string {
	if strings.Contains(raw, " ") {
		return fmt.Sprintf("%s arrived as %q: an unescaped + in a query string decodes to a "+
			"space, so a positive offset has to be percent encoded, as in %s",
			name, raw, encodedExample(name))
	}
	return fmt.Sprintf("%s must be an RFC3339 timestamp with an explicit offset, as in "+
		"%s or %s=2026-03-01T00:00:00Z; got %q",
		name, encodedExample(name), name, raw)
}

func encodedExample(name string) string {
	return name + "=2026-03-01T00:00:00%2B03:00"
}

// parseCurrencyParam checks the code against ISO 4217 rather than against its
// shape. Three letters is not enough: a typo like SAT would pass, match nothing
// and come back as a row of zeros, which reads as "no business here" instead of
// "you asked for a currency that does not exist".
func parseCurrencyParam(query url.Values) (string, *paramError) {
	raw := strings.ToUpper(strings.TrimSpace(query.Get("currency")))
	if raw == "" {
		return "", nil
	}

	if !money.Known(raw) {
		return "", &paramError{
			code: "invalid_currency",
			message: fmt.Sprintf("currency must be a known ISO 4217 alphabetic code "+
				"such as SAR; got %q", raw),
		}
	}
	return raw, nil
}
