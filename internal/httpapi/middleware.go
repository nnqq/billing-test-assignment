package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (a *API) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
		defer cancel()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		a.log.InfoContext(r.Context(), "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.Int("status", recorder.status),
			slog.Duration("took", time.Since(started)),
		)
	})
}

func (a *API) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			a.log.ErrorContext(r.Context(), "panic serving request",
				slog.String("path", r.URL.Path),
				slog.Any("panic", recovered),
			)
			a.writeJSON(w, r, http.StatusInternalServerError, errorResponse{Error: errorBody{
				Code:    "internal_error",
				Message: "unexpected server error",
			}})
		}()

		next.ServeHTTP(w, r)
	})
}
