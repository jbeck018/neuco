package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/linear"
	"github.com/neuco-ai/neuco/internal/store"
)

// LinearSyncWorker fetches issues from a natively-connected Linear workspace
// and inserts them as signals. The access token is stored in the integration's
// Config["access_token"] field.
type LinearSyncWorker struct {
	river.WorkerDefaults[LinearSyncJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewLinearSyncWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *LinearSyncWorker {
	return &LinearSyncWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}

func (w *LinearSyncWorker) Work(ctx context.Context, job *river.Job[LinearSyncJobArgs]) error {
	start := time.Now()
	args := job.Args

	StartTask(ctx, w.store, args.TaskID)

	slog.Info("linear_sync: starting",
		"project_id", args.ProjectID,
		"integration_id", args.IntegrationID,
	)

	// Fetch the integration record to get the access token.
	intg, err := w.store.GetIntegrationInternal(ctx, args.IntegrationID)
	if err != nil {
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("linear_sync: get integration: %w", err)
	}

	accessToken, _ := intg.Config["access_token"].(string)
	if accessToken == "" {
		err := fmt.Errorf("linear_sync: no access_token in integration config")
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return err
	}

	// Determine incremental sync window.
	var sinceTimestamp int64
	if intg.LastSyncAt != nil {
		sinceTimestamp = intg.LastSyncAt.Unix()
	}

	// Fetch issues from Linear.
	client := linear.NewClient(w.cfg.LinearClientID, w.cfg.LinearClientSecret)
	issues, err := client.ListIssues(ctx, accessToken, sinceTimestamp)
	if err != nil {
		slog.Error("linear_sync: fetch issues failed", "error", err)
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("linear_sync: fetch issues: %w", err)
	}

	// Convert to signals and insert, skipping exact duplicates.
	insertedCount := 0
	dedupCount := 0
	for _, issue := range issues {
		sig := linear.IssueToSignal(issue, args.ProjectID)
		if _, insertErr := w.store.InsertSignal(ctx, sig); insertErr != nil {
			if errors.Is(insertErr, store.ErrDuplicateSignal) {
				dedupCount++
				continue
			}
			slog.Error("linear_sync: insert signal failed",
				"issue_id", issue.ID,
				"error", insertErr,
			)
			continue
		}
		insertedCount++
	}

	slog.Info("linear_sync: signals inserted",
		"total_fetched", len(issues),
		"total_inserted", insertedCount,
		"deduplicated", dedupCount,
	)

	// Stamp last_sync_at.
	if err := w.store.UpdateIntegrationLastSync(ctx, args.ProjectID, args.IntegrationID, time.Now().UTC()); err != nil {
		slog.Warn("linear_sync: failed to update last_sync_at", "error", err)
	}

	// Chain embed job.
	if insertedCount > 0 {
		w.enqueueEmbed(ctx, args)
	}

	CompleteTask(ctx, w.store, args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, args.RunID)

	return nil
}

func (w *LinearSyncWorker) enqueueEmbed(ctx context.Context, args LinearSyncJobArgs) {
	client := w.jobCtx.Client()
	if client == nil {
		slog.Warn("linear_sync: river client not available, skipping embed enqueue")
		return
	}

	embedArgs := EmbedJobArgs{
		ProjectID: args.ProjectID,
		RunID:     args.RunID,
	}

	if args.RunID.String() != "00000000-0000-0000-0000-000000000000" {
		run, err := w.store.GetPipelineRun(ctx, args.RunID)
		if err == nil {
			for _, t := range run.Tasks {
				if t.Name == "embed" {
					embedArgs.TaskID = t.ID
					break
				}
			}
		}
	}

	if _, err := client.Insert(ctx, embedArgs, nil); err != nil {
		slog.Warn("linear_sync: failed to enqueue embed job", "error", err)
	}
}
