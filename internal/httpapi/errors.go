package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// statusClientClosedRequest is nginx's 499. It is not in the RFC, but it keeps
// a caller who hung up out of the 5xx error budget, which is the difference
// between an alert that means something and one that fires on every closed tab.
const statusClientClosedRequest = 499

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// writeError keeps the wire format uniform and, for 5xx, records the cause
// server side rather than leaking it to the caller.
func (a *API) writeError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	code, message string,
	cause error,
) {
	if cause != nil && status >= http.StatusInternalServerError {
		a.log.ErrorContext(r.Context(), "request failed",
			slog.String("path", r.URL.Path),
			slog.String("code", code),
			slog.Any("error", cause),
		)
	}

	a.writeJSON(w, r, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

// writeStoreError classifies a failed storage call. Reporting all three cases
// as 500 conflates a slow query we own, a caller who walked away, and a genuine
// defect: the first two are not bugs and must not page anybody.
func (a *API) writeStoreError(w http.ResponseWriter, r *http.Request, message string, cause error) {
	ctxErr := r.Context().Err()

	switch {
	case errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctxErr, context.DeadlineExceeded):
		a.log.WarnContext(r.Context(), "request timed out",
			slog.String("path", r.URL.Path),
			slog.Duration("timeout", a.requestTimeout),
			slog.Any("error", cause),
		)
		a.writeError(w, r, http.StatusGatewayTimeout, "timeout",
			"the request took longer than the service allows", nil)

	case errors.Is(cause, context.Canceled) || errors.Is(ctxErr, context.Canceled):
		a.log.InfoContext(r.Context(), "request cancelled by the client",
			slog.String("path", r.URL.Path),
		)
		a.writeError(w, r, statusClientClosedRequest, "client_closed_request",
			"the client closed the request before it was answered", nil)

	default:
		a.writeError(w, r, http.StatusInternalServerError, "internal_error", message, cause)
	}
}
