package observability

import (
	"log/slog"
	"os"
)

// InitLogging sets up structured JSON logging as the default slog handler.
// The service name is added to every log line.
func InitLogging(service string, env string) {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: env != "development",
	})

	logger := slog.New(handler).With(slog.String("service", service))
	slog.SetDefault(logger)
}
