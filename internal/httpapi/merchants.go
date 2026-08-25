package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	defaultMerchantLimit = 100
	maxMerchantLimit     = 1000
)

// merchantsResponse pages. NextAfter is absent on the last page; while it is
// present the caller has not seen every merchant yet.
type merchantsResponse struct {
	MerchantIDs []string `json:"merchant_ids"`
	NextAfter   string   `json:"next_after,omitempty"`
}

func (a *API) handleMerchants(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	limit, paramErr := parseLimitParam(query)
	if paramErr != nil {
		a.writeError(w, r, http.StatusBadRequest, paramErr.code, paramErr.message, nil)
		return
	}

	page, err := a.store.MerchantIDs(r.Context(), query.Get("after"), limit)
	if err != nil {
		a.writeStoreError(w, r, "could not list merchants", err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, merchantsResponse{
		MerchantIDs: page.IDs,
		NextAfter:   page.NextAfter,
	})
}

func parseLimitParam(query url.Values) (int, *paramError) {
	raw := query.Get("limit")
	if raw == "" {
		return defaultMerchantLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxMerchantLimit {
		return 0, &paramError{
			code: "invalid_limit",
			message: fmt.Sprintf("limit must be a whole number between 1 and %d; got %q",
				maxMerchantLimit, raw),
		}
	}
	return limit, nil
}
