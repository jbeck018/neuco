package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/jira"
	"github.com/neuco-ai/neuco/internal/store"
)

// JiraSyncWorker fetches issues from a natively-connected Jira Cloud site
// and inserts them as signals. The access token and cloud_id are stored in
// the integration's Config map.
type JiraSyncWorker struct {
	river.WorkerDefaults[JiraSyncJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewJiraSyncWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *JiraSyncWorker {
	return &JiraSyncWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}

func (w *JiraSyncWorker) Work(ctx context.Context, job *river.Job[JiraSyncJobArgs]) error {
	start := time.Now()
	args := job.Args

	StartTask(ctx, w.store, args.TaskID)

	slog.Info("jira_sync: starting",
		"project_id", args.ProjectID,
		"integration_id", args.IntegrationID,
	)

	// Fetch the integration record to get the access token and cloud ID.
	intg, err := w.store.GetIntegrationInternal(ctx, args.IntegrationID)
	if err != nil {
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("jira_sync: get integration: %w", err)
	}

	accessToken, _ := intg.Config["access_token"].(string)
	if accessToken == "" {
		err := fmt.Errorf("jira_sync: no access_token in integration config")
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return err
	}

	cloudID, _ := intg.Config["cloud_id"].(string)
	if cloudID == "" {
		err := fmt.Errorf("jira_sync: no cloud_id in integration config")
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return err
	}

	// Determine incremental sync window.
	var sinceTimestamp int64
	if intg.LastSyncAt != nil {
		sinceTimestamp = intg.LastSyncAt.Unix()
	}

	// Fetch issues from Jira.
	client := jira.NewClient(w.cfg.JiraClientID, w.cfg.JiraClientSecret)
	issues, err := client.ListIssues(ctx, accessToken, cloudID, sinceTimestamp)
	if err != nil {
		slog.Error("jira_sync: fetch issues failed", "error", err)
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("jira_sync: fetch issues: %w", err)
	}

	// Convert to signals and insert, skipping exact duplicates.
	insertedCount := 0
	dedupCount := 0
	for _, issue := range issues {
		sig := jira.IssueToSignal(issue, args.ProjectID)
		if _, insertErr := w.store.InsertSignal(ctx, sig); insertErr != nil {
			if errors.Is(insertErr, store.ErrDuplicateSignal) {
				dedupCount++
				continue
			}
			slog.Error("jira_sync: insert signal failed",
				"issue_key", issue.Key,
				"error", insertErr,
			)
			continue
		}
		insertedCount++
	}

	slog.Info("jira_sync: signals inserted",
		"total_fetched", len(issues),
		"total_inserted", insertedCount,
		"deduplicated", dedupCount,
	)

	// Stamp last_sync_at.
	if err := w.store.UpdateIntegrationLastSync(ctx, args.ProjectID, args.IntegrationID, time.Now().UTC()); err != nil {
		slog.Warn("jira_sync: failed to update last_sync_at", "error", err)
	}

	// Chain embed job.
	if insertedCount > 0 {
		w.enqueueEmbed(ctx, args)
	}

	CompleteTask(ctx, w.store, args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, args.RunID)

	return nil
}

func (w *JiraSyncWorker) enqueueEmbed(ctx context.Context, args JiraSyncJobArgs) {
	client := w.jobCtx.Client()
	if client == nil {
		slog.Warn("jira_sync: river client not available, skipping embed enqueue")
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
		slog.Warn("jira_sync: failed to enqueue embed job", "error", err)
	}
}
