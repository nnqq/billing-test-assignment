package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/storage/mongodb"
	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

type fakeStore struct {
	totals       []transaction.CurrencyTotals
	merchants    []string
	exists       bool
	summaryErr   error
	existsErr    error
	merchantsErr error
	pingErr      error
	lastFilter   transaction.Filter
	lastAfter    string
	lastLimit    int
	summaryCall  int
	existsCall   int
}

func (f *fakeStore) Summary(
	_ context.Context,
	filter transaction.Filter,
) ([]transaction.CurrencyTotals, error) {
	f.lastFilter = filter
	f.summaryCall++
	return f.totals, f.summaryErr
}

func (f *fakeStore) MerchantExists(_ context.Context, _ string) (bool, error) {
	f.existsCall++
	return f.exists, f.existsErr
}

func (f *fakeStore) MerchantIDs(
	_ context.Context,
	after string,
	limit int,
) (mongodb.MerchantPage, error) {
	f.lastAfter = after
	f.lastLimit = limit
	return mongodb.MerchantPage{IDs: f.merchants}, f.merchantsErr
}

func (f *fakeStore) Ping(_ context.Context) error {
	return f.pingErr
}

func newTestAPI(store Store) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(store, log, 5*time.Second).Handler()
}

func TestSummaryOK(t *testing.T) {
	store := &fakeStore{
		exists: true,
		totals: []transaction.CurrencyTotals{
			{
				Currency:        "SAR",
				TurnoverMinor:   43797410,
				RefundsMinor:    2215100,
				NetMinor:        41582310,
				FeeMinor:        706899,
				SaleCount:       123,
				RefundCount:     8,
				FeeMissingCount: 2,
			},
		},
	}

	request := httptest.NewRequest(http.MethodGet,
		"/merchants/M-1001/summary?from=2026-03-01T00:00:00%2B03:00&to=2026-04-01T00:00:00%2B03:00",
		nil)
	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}

	var body summaryResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.MerchantID != "M-1001" {
		t.Errorf("merchant_id = %q, want M-1001", body.MerchantID)
	}
	if len(body.Totals) != 1 || body.Totals[0].FeeMinor != 706899 {
		t.Fatalf("totals = %+v", body.Totals)
	}
	if body.Totals[0].RefundsMinor != 2215100 {
		t.Errorf("refunds_minor = %d, want 2215100", body.Totals[0].RefundsMinor)
	}
	if body.Totals[0].FeeMissingCount != 2 {
		t.Errorf("fee_missing_count = %d, want 2", body.Totals[0].FeeMissingCount)
	}

	// Rows in the answer already prove the merchant exists, so the common path
	// must not pay for a second lookup.
	if store.existsCall != 0 {
		t.Errorf("existence was queried %d times, want 0 when the summary is not empty",
			store.existsCall)
	}

	// +03:00 has to reach the store as UTC, not as the wall clock.
	wantFrom := time.Date(2026, 2, 28, 21, 0, 0, 0, time.UTC)
	if store.lastFilter.From == nil || !store.lastFilter.From.Equal(wantFrom) {
		t.Errorf("filter.From = %v, want %v", store.lastFilter.From, wantFrom)
	}
	wantTo := time.Date(2026, 3, 31, 21, 0, 0, 0, time.UTC)
	if store.lastFilter.To == nil || !store.lastFilter.To.Equal(wantTo) {
		t.Errorf("filter.To = %v, want %v", store.lastFilter.To, wantTo)
	}
}

func TestSummaryWithoutPeriod(t *testing.T) {
	store := &fakeStore{exists: true}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/merchants/M-1001/summary", nil)
	newTestAPI(store).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if store.lastFilter.From != nil || store.lastFilter.To != nil {
		t.Errorf("filter period = (%v, %v), want both nil",
			store.lastFilter.From, store.lastFilter.To)
	}
	if recorder.Body.String() == "" {
		t.Error("expected a body")
	}
}

func TestSummaryCurrencyFilter(t *testing.T) {
	store := &fakeStore{exists: true}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"/merchants/M-1001/summary?currency=sar", nil)
	newTestAPI(store).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if store.lastFilter.Currency != "SAR" {
		t.Errorf("filter.Currency = %q, want SAR", store.lastFilter.Currency)
	}
}

func TestSummaryEmptyPeriodWithCurrency(t *testing.T) {
	store := &fakeStore{exists: true}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/merchants/M-1003/summary?currency=SAR&from=2026-03-15T00:00:00Z&to=2026-03-16T00:00:00Z",
		nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}

	var body summaryResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Totals) != 1 {
		t.Fatalf("totals = %+v, want one zeroed row", body.Totals)
	}
	if body.Totals[0].Currency != "SAR" || body.Totals[0].FeeMinor != 0 {
		t.Errorf("totals[0] = %+v, want a zeroed SAR row", body.Totals[0])
	}
}

func TestSummaryEmptyPeriodWithoutCurrency(t *testing.T) {
	store := &fakeStore{exists: true}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants/M-1003/summary", nil))

	var body summaryResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Totals == nil {
		t.Error("totals must serialise as [], not null")
	}
	if len(body.Totals) != 0 {
		t.Errorf("totals = %+v, want empty", body.Totals)
	}
}

func TestSummaryBadRequests(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		wantCode string
	}{
		{
			name:     "date without offset",
			target:   "/merchants/M-1001/summary?from=2026-03-01",
			wantCode: "invalid_period",
		},
		{
			name:     "garbage timestamp",
			target:   "/merchants/M-1001/summary?to=yesterday",
			wantCode: "invalid_period",
		},
		{
			name:     "from equals to",
			target:   "/merchants/M-1001/summary?from=2026-03-01T00:00:00Z&to=2026-03-01T00:00:00Z",
			wantCode: "invalid_period",
		},
		{
			name:     "from after to",
			target:   "/merchants/M-1001/summary?from=2026-04-01T00:00:00Z&to=2026-03-01T00:00:00Z",
			wantCode: "invalid_period",
		},
		{
			name:     "bad currency",
			target:   "/merchants/M-1001/summary?currency=rubles",
			wantCode: "invalid_currency",
		},
		{
			name:     "currency that is not a currency",
			target:   "/merchants/M-1001/summary?currency=SAT",
			wantCode: "invalid_currency",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{exists: true}
			recorder := httptest.NewRecorder()
			newTestAPI(store).ServeHTTP(recorder,
				httptest.NewRequest(http.MethodGet, c.target, nil))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
			}

			var body errorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != c.wantCode {
				t.Errorf("error code = %q, want %q", body.Error.Code, c.wantCode)
			}
			if store.summaryCall != 0 {
				t.Error("store must not be queried for an invalid request")
			}
		})
	}
}

// The single most likely 400 this API will ever return: a caller pastes
// +03:00 into a query string, it decodes to a space, and the old message
// answered by recommending the very string that had just failed.
func TestSummaryUnescapedOffsetIsDiagnosed(t *testing.T) {
	store := &fakeStore{exists: true}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/merchants/M-1001/summary?from=2026-03-01T00:00:00+03:00", nil))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
	}

	var body errorResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "invalid_period" {
		t.Fatalf("error code = %q, want invalid_period", body.Error.Code)
	}

	if !strings.Contains(body.Error.Message, "%2B") {
		t.Errorf("message = %q, want it to name the percent encoded offset", body.Error.Message)
	}

	// Whatever the message suggests has to survive a round trip through a query
	// string, or it sends the caller back round the same loop.
	suggestions := offsetsIn(body.Error.Message, "from")
	if len(suggestions) == 0 {
		t.Fatalf("message = %q, want it to suggest a usable value", body.Error.Message)
	}

	for _, suggestion := range suggestions {
		parsed, parseErr := url.ParseQuery("from=" + suggestion)
		if parseErr != nil {
			t.Fatalf("suggestion %q is not a usable query value: %v", suggestion, parseErr)
		}
		_, timeErr := time.Parse(time.RFC3339, parsed.Get("from"))
		if timeErr != nil {
			t.Errorf("suggestion %q decodes to %q, which the api rejects: %v",
				suggestion, parsed.Get("from"), timeErr)
		}
	}
}

// offsetsIn picks the suggestions out of a hint so the test checks what the
// message actually tells the caller to send. Suggestions carry the parameter
// name, which is what separates them from the value echoed back at the caller.
func offsetsIn(message, name string) []string {
	var found []string
	for _, word := range strings.Fields(message) {
		trimmed := strings.Trim(word, ",;.\"")
		if strings.HasPrefix(trimmed, name+"=") {
			found = append(found, strings.TrimPrefix(trimmed, name+"="))
		}
	}
	return found
}

func TestSummaryUnknownMerchant(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestAPI(&fakeStore{exists: false}).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants/M-9999/summary", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body)
	}

	var body errorResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "merchant_not_found" {
		t.Errorf("error code = %q, want merchant_not_found", body.Error.Code)
	}
}

func TestSummaryStorageFailureHidesCause(t *testing.T) {
	store := &fakeStore{exists: true, summaryErr: errors.New("mongo is on fire at 10.0.0.4")}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants/M-1001/summary", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	bodyText := recorder.Body.String()
	if strings.Contains(bodyText, "10.0.0.4") {
		t.Errorf("internal detail leaked to the client: %s", bodyText)
	}
}

func TestReadyReflectsStorage(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestAPI(&fakeStore{pingErr: errors.New("down")}).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.Code)
	}
}

func TestMerchants(t *testing.T) {
	store := &fakeStore{merchants: []string{"M-1001", "M-1002"}}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var body merchantsResponse
	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.MerchantIDs) != 2 {
		t.Errorf("merchant_ids = %v, want two entries", body.MerchantIDs)
	}
	if store.lastLimit != defaultMerchantLimit {
		t.Errorf("limit = %d, want the default %d", store.lastLimit, defaultMerchantLimit)
	}
}

func TestMerchantsPagination(t *testing.T) {
	store := &fakeStore{merchants: []string{"M-1003"}}

	recorder := httptest.NewRecorder()
	newTestAPI(store).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants?limit=25&after=M-1002", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if store.lastLimit != 25 || store.lastAfter != "M-1002" {
		t.Errorf("store called with (after=%q, limit=%d), want (M-1002, 25)",
			store.lastAfter, store.lastLimit)
	}
}

func TestMerchantsRejectsBadLimit(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc", "1001"} {
		t.Run(raw, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestAPI(&fakeStore{}).ServeHTTP(recorder,
				httptest.NewRequest(http.MethodGet, "/merchants?limit="+raw, nil))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
			}

			var body errorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != "invalid_limit" {
				t.Errorf("error code = %q, want invalid_limit", body.Error.Code)
			}
		})
	}
}

// A wrong verb on a path that exists is not a missing route. Reporting 404
// sends the caller hunting for a typo in the path instead of in the method.
func TestMethodNotAllowed(t *testing.T) {
	cases := []struct{ method, target string }{
		{http.MethodPost, "/merchants"},
		{http.MethodDelete, "/merchants/M-1001/summary"},
		{http.MethodPut, "/healthz"},
		{http.MethodPost, "/readyz"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestAPI(&fakeStore{exists: true}).ServeHTTP(recorder,
				httptest.NewRequest(c.method, c.target, nil))

			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405: %s", recorder.Code, recorder.Body)
			}
			if recorder.Header().Get("Allow") != http.MethodGet {
				t.Errorf("Allow = %q, want GET", recorder.Header().Get("Allow"))
			}

			var body errorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != "method_not_allowed" {
				t.Errorf("error code = %q, want method_not_allowed", body.Error.Code)
			}
		})
	}
}

func TestUnknownRouteStillNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestAPI(&fakeStore{}).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", recorder.Code)
	}
}

// A deadline the service imposed on itself is not an internal error, and a
// caller who hung up is not an error at all. Reporting both as 500 makes the
// 5xx rate meaningless as an alert.
func TestStoreErrorsAreClassified(t *testing.T) {
	cases := []struct {
		name       string
		storeErr   error
		wantStatus int
		wantCode   string
	}{
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
		{"cancelled", context.Canceled, statusClientClosedRequest, "client_closed_request"},
		{"genuine", errors.New("index corrupted"), http.StatusInternalServerError, "internal_error"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{exists: true, summaryErr: fmt.Errorf("aggregate: %w", c.storeErr)}

			recorder := httptest.NewRecorder()
			newTestAPI(store).ServeHTTP(recorder,
				httptest.NewRequest(http.MethodGet, "/merchants/M-1001/summary", nil))

			if recorder.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, c.wantStatus, recorder.Body)
			}

			var body errorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != c.wantCode {
				t.Errorf("error code = %q, want %q", body.Error.Code, c.wantCode)
			}
		})
	}
}

// The timeout the middleware installs has to surface as a timeout, not as an
// internal error, even when the store reports the bare context error.
func TestRequestTimeoutIsAGatewayTimeout(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(blockingStore{}, log, 20*time.Millisecond).Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/merchants/M-1001/summary", nil))

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", recorder.Code, recorder.Body)
	}
}

type blockingStore struct{}

func (blockingStore) Summary(
	ctx context.Context,
	_ transaction.Filter,
) ([]transaction.CurrencyTotals, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingStore) MerchantExists(context.Context, string) (bool, error) { return true, nil }

func (blockingStore) MerchantIDs(context.Context, string, int) (mongodb.MerchantPage, error) {
	return mongodb.MerchantPage{}, nil
}

func (blockingStore) Ping(context.Context) error { return nil }
