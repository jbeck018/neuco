package middleware

import (
	"log/slog"
	"net/http"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

// responseRecorder wraps http.ResponseWriter to capture the status code written
// by a downstream handler.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// RequestLogger returns a middleware that writes a structured slog log line for
// every HTTP request, including method, path, status, duration, user_id, org_id,
// and request_id.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := chiMiddleware.GetReqID(r.Context())

			rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rec, r)

			userID := UserIDFromCtx(r.Context())
			orgID := OrgIDFromCtx(r.Context())
			duration := time.Since(start)

			logger.InfoContext(r.Context(), "http request",
				slog.String("request_id", reqID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.statusCode),
				slog.Int64("duration_ms", duration.Milliseconds()),
				slog.String("user_id", userID.String()),
				slog.String("org_id", orgID.String()),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}
