package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/slack"
	"github.com/neuco-ai/neuco/internal/store"
)

// SlackSyncWorker fetches messages from channels in a natively-connected Slack
// workspace and inserts them as signals. The bot access token is stored in the
// integration's Config["access_token"] field.
type SlackSyncWorker struct {
	river.WorkerDefaults[SlackSyncJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewSlackSyncWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *SlackSyncWorker {
	return &SlackSyncWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}

func (w *SlackSyncWorker) Work(ctx context.Context, job *river.Job[SlackSyncJobArgs]) error {
	start := time.Now()
	args := job.Args

	StartTask(ctx, w.store, args.TaskID)

	slog.Info("slack_sync: starting",
		"project_id", args.ProjectID,
		"integration_id", args.IntegrationID,
	)

	// Fetch the integration record to get the access token.
	intg, err := w.store.GetIntegrationInternal(ctx, args.IntegrationID)
	if err != nil {
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("slack_sync: get integration: %w", err)
	}

	accessToken, _ := intg.Config["access_token"].(string)
	if accessToken == "" {
		err := fmt.Errorf("slack_sync: no access_token in integration config")
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return err
	}

	// Determine incremental sync window.
	var sinceTimestamp float64
	if intg.LastSyncAt != nil {
		sinceTimestamp = float64(intg.LastSyncAt.Unix())
	}

	// Discover channels the bot is a member of.
	channels, err := slack.ListChannels(ctx, accessToken)
	if err != nil {
		slog.Error("slack_sync: list channels failed", "error", err)
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("slack_sync: list channels: %w", err)
	}

	slog.Info("slack_sync: discovered channels", "count", len(channels))

	// Fetch messages from each channel and insert as signals.
	insertedCount := 0
	dedupCount := 0
	for _, ch := range channels {
		messages, err := slack.FetchChannelHistory(ctx, accessToken, ch.ID, sinceTimestamp)
		if err != nil {
			slog.Error("slack_sync: fetch history failed",
				"channel_id", ch.ID,
				"channel_name", ch.Name,
				"error", err,
			)
			continue
		}

		for _, msg := range messages {
			sig := slack.MessageToSignal(msg, ch.Name, ch.ID, args.ProjectID)
			if _, insertErr := w.store.InsertSignal(ctx, sig); insertErr != nil {
				if errors.Is(insertErr, store.ErrDuplicateSignal) {
					dedupCount++
					continue
				}
				slog.Error("slack_sync: insert signal failed",
					"channel_id", ch.ID,
					"ts", msg.TS,
					"error", insertErr,
				)
				continue
			}
			insertedCount++
		}
	}

	slog.Info("slack_sync: signals inserted",
		"channels_synced", len(channels),
		"total_inserted", insertedCount,
		"deduplicated", dedupCount,
	)

	// Stamp last_sync_at.
	if err := w.store.UpdateIntegrationLastSync(ctx, args.ProjectID, args.IntegrationID, time.Now().UTC()); err != nil {
		slog.Warn("slack_sync: failed to update last_sync_at", "error", err)
	}

	// Chain embed job.
	if insertedCount > 0 {
		w.enqueueEmbed(ctx, args)
	}

	CompleteTask(ctx, w.store, args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, args.RunID)

	return nil
}

func (w *SlackSyncWorker) enqueueEmbed(ctx context.Context, args SlackSyncJobArgs) {
	client := w.jobCtx.Client()
	if client == nil {
		slog.Warn("slack_sync: river client not available, skipping embed enqueue")
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
		slog.Warn("slack_sync: failed to enqueue embed job", "error", err)
	}
}
