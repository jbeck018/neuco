package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/getsentry/sentry-go"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

// SentryRecovery is a panic recovery middleware that reports panics to Sentry
// and logs them as structured errors. It replaces chi's default Recoverer.
func SentryRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rv := recover(); rv != nil {
				stack := string(debug.Stack())
				reqID := chiMiddleware.GetReqID(r.Context())

				slog.ErrorContext(r.Context(), "panic recovered",
					slog.String("panic", fmt.Sprint(rv)),
					slog.String("request_id", reqID),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("stack", stack),
				)

				if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
					hub.RecoverWithContext(r.Context(), rv)
				} else {
					sentry.CurrentHub().Clone().RecoverWithContext(r.Context(), rv)
				}

				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
