// Package httpapi exposes the billing read model over HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nnqq/billing-test-assignment/internal/storage/mongodb"
	"github.com/nnqq/billing-test-assignment/internal/transaction"
)

type Store interface {
	Summary(ctx context.Context, filter transaction.Filter) ([]transaction.CurrencyTotals, error)
	MerchantExists(ctx context.Context, merchantID string) (bool, error)
	MerchantIDs(ctx context.Context, after string, limit int) (mongodb.MerchantPage, error)
	Ping(ctx context.Context) error
}

type API struct {
	store          Store
	log            *slog.Logger
	requestTimeout time.Duration
}

func New(store Store, log *slog.Logger, requestTimeout time.Duration) *API {
	return &API{store: store, log: log, requestTimeout: requestTimeout}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleNotFound)

	a.route(mux, "/healthz", http.MethodGet, a.handleHealth)
	a.route(mux, "/readyz", http.MethodGet, a.handleReady)
	a.route(mux, "/merchants", http.MethodGet, a.handleMerchants)
	a.route(mux, "/merchants/{merchant_id}/summary", http.MethodGet, a.handleSummary)

	return a.recoverPanic(a.logRequest(a.withTimeout(mux)))
}

// route registers a handler for one method and answers every other method on
// the same path with 405. Without that second registration the catch-all "/"
// wins the match and reports a wrong method as a missing route, which sends the
// caller looking for a typo in the path instead of in the verb.
func (a *API) route(mux *http.ServeMux, pattern, method string, handler http.HandlerFunc) {
	mux.HandleFunc(method+" "+pattern, handler)
	mux.HandleFunc(pattern, a.methodNotAllowed(method))
}

func (a *API) methodNotAllowed(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allowed)
		a.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("%s is not allowed on %s, use %s", r.Method, r.URL.Path, allowed), nil)
	}
}

func (a *API) handleNotFound(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, http.StatusNotFound, "not_found",
		fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path), nil)
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	err := a.store.Ping(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "storage_unavailable",
			"storage is not reachable", err)
		return
	}
	a.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON commits the status line before the body, so an encoder failure can
// no longer become an error response. Logging it is what keeps a truncated
// answer visible instead of looking like a clean 200 to everyone involved.
func (a *API) writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(body)
	if err != nil {
		a.log.ErrorContext(r.Context(), "write response body",
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Any("error", err),
		)
	}
}
