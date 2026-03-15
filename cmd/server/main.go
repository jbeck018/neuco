package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/neuco-ai/neuco/internal/api"
	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/jobs"
	"github.com/neuco-ai/neuco/internal/observability"
	"github.com/neuco-ai/neuco/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	observability.InitLogging("neuco-api", cfg.AppEnv)
	flushSentry := observability.InitSentry(cfg, "neuco-api")
	defer flushSentry()

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	poolConfig.MaxConns = 25
	poolConfig.MinConns = 5
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to database")

	s := store.New(pool)

	// River client in insert-only mode — workers must still be registered
	// so that River recognises the job kinds during Insert.
	workers := river.NewWorkers()
	jobCtx := jobs.RegisterAllWorkers(workers, s, cfg)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers,
	})
	if err != nil {
		slog.Error("failed to create river client", "error", err)
		os.Exit(1)
	}

	jobCtx.SetClient(riverClient)

	registry := codegen.NewProviderRegistry(codegen.ClaudeCodeProvider{})
	deps := api.NewDeps(s, riverClient, jobCtx, cfg, pool, registry)
	// NewRouter constructs the full Chi router with all middleware and routes.
	handler := api.NewRouter(deps, slog.Default())
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second, // long for SSE
		IdleTimeout:  120 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		slog.Info("starting API server", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down API server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}
	slog.Info("API server stopped")
}
