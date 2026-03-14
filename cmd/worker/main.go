package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

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

	observability.InitLogging("neuco-worker", cfg.AppEnv)
	flushSentry := observability.InitSentry(cfg, "neuco-worker")
	defer flushSentry()

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	poolConfig.MaxConns = 15
	poolConfig.MinConns = 2
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

	workers := river.NewWorkers()
	jobCtx := jobs.RegisterAllWorkers(workers, s, cfg)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			"ingest":    {MaxWorkers: 5},
			"synthesis": {MaxWorkers: 2},
			"codegen":   {MaxWorkers: 3},
			"default":   {MaxWorkers: 10},
		},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(7*24*time.Hour), // weekly
				func() (river.JobArgs, *river.InsertOpts) {
					return jobs.DigestAllProjectsJobArgs{}, &river.InsertOpts{Queue: "synthesis"}
				},
				&river.PeriodicJobOpts{RunOnStart: false},
			),
			river.NewPeriodicJob(
				river.PeriodicInterval(6*time.Hour), // sync integrations every 6 hours
				func() (river.JobArgs, *river.InsertOpts) {
					return jobs.SyncAllIntegrationsJobArgs{}, &river.InsertOpts{Queue: "default"}
				},
				&river.PeriodicJobOpts{RunOnStart: false},
			),
			river.NewPeriodicJob(
				river.PeriodicInterval(7*24*time.Hour), // weekly digest emails
				func() (river.JobArgs, *river.InsertOpts) {
					return jobs.DigestEmailsJobArgs{}, &river.InsertOpts{Queue: "default"}
				},
				&river.PeriodicJobOpts{RunOnStart: false},
			),
		},
	})
	if err != nil {
		slog.Error("failed to create river client", "error", err)
		os.Exit(1)
	}

	// Store the river client reference so workers can chain jobs
	jobCtx.SetClient(riverClient)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := riverClient.Start(ctx); err != nil {
		slog.Error("failed to start river client", "error", err)
		os.Exit(1)
	}
	slog.Info("worker started, processing jobs...")

	<-ctx.Done()
	slog.Info("shutting down worker, draining in-flight jobs...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := riverClient.Stop(shutdownCtx); err != nil {
		slog.Error("error stopping river client", "error", err)
	}
	slog.Info("worker stopped cleanly")
}
