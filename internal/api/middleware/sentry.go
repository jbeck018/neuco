package middleware

import (
	"net/http"

	"github.com/getsentry/sentry-go"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

// SentryContext clones the Sentry hub into the request context and sets
// request-level tags (request_id, method, path). Downstream code and the
// recovery middleware will pick up this hub automatically.
func SentryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetRequest(r)
		hub.Scope().SetTag("request_id", chiMiddleware.GetReqID(r.Context()))

		ctx := sentry.SetHubOnContext(r.Context(), hub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SetSentryUser sets user-level tags on the Sentry scope. Call this after
// authentication middleware has set user and org IDs in context.
func SetSentryUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
			userID := UserIDFromCtx(r.Context())
			orgID := OrgIDFromCtx(r.Context())
			hub.Scope().SetUser(sentry.User{
				ID: userID.String(),
			})
			hub.Scope().SetTag("org_id", orgID.String())
		}
		next.ServeHTTP(w, r)
	})
}
