package observability

import (
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/neuco-ai/neuco/internal/config"
)

// InitSentry initialises the Sentry SDK. If no DSN is configured it is a no-op.
// Returns a flush function that should be deferred by the caller.
func InitSentry(cfg *config.Config, service string) func() {
	if cfg.SentryDSN == "" {
		slog.Info("sentry disabled (no SENTRY_DSN)")
		return func() {}
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.SentryDSN,
		Environment:      cfg.AppEnv,
		Release:          service,
		TracesSampleRate: 0.2,
		AttachStacktrace: true,
	})
	if err != nil {
		slog.Error("failed to initialise sentry", "error", err)
		return func() {}
	}

	slog.Info("sentry initialised", "service", service, "env", cfg.AppEnv)
	return func() { sentry.Flush(2 * time.Second) }
}
