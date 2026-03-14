package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/intercom"
	"github.com/neuco-ai/neuco/internal/store"
)

// IntercomSyncWorker fetches conversations from a natively-connected Intercom
// workspace and inserts them as signals. The access token is stored in the
// integration's Config["access_token"] field.
type IntercomSyncWorker struct {
	river.WorkerDefaults[IntercomSyncJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewIntercomSyncWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *IntercomSyncWorker {
	return &IntercomSyncWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}

func (w *IntercomSyncWorker) Work(ctx context.Context, job *river.Job[IntercomSyncJobArgs]) error {
	start := time.Now()
	args := job.Args

	StartTask(ctx, w.store, args.TaskID)

	slog.Info("intercom_sync: starting",
		"project_id", args.ProjectID,
		"integration_id", args.IntegrationID,
	)

	// Fetch the integration record to get the access token.
	intg, err := w.store.GetIntegrationInternal(ctx, args.IntegrationID)
	if err != nil {
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("intercom_sync: get integration: %w", err)
	}

	accessToken, _ := intg.Config["access_token"].(string)
	if accessToken == "" {
		err := fmt.Errorf("intercom_sync: no access_token in integration config")
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return err
	}

	// Determine incremental sync window.
	var sinceTimestamp int64
	if intg.LastSyncAt != nil {
		sinceTimestamp = intg.LastSyncAt.Unix()
	}

	// Fetch conversations from Intercom.
	client := intercom.NewClient(w.cfg.IntercomClientID, w.cfg.IntercomClientSecret)
	conversations, err := client.ListConversations(ctx, accessToken, sinceTimestamp)
	if err != nil {
		slog.Error("intercom_sync: fetch conversations failed", "error", err)
		FailTask(ctx, w.store, args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, args.RunID)
		return fmt.Errorf("intercom_sync: fetch conversations: %w", err)
	}

	// Convert to signals and insert, skipping exact duplicates.
	insertedCount := 0
	dedupCount := 0
	for _, conv := range conversations {
		sig := intercom.ConversationToSignal(conv, args.ProjectID)
		if _, insertErr := w.store.InsertSignal(ctx, sig); insertErr != nil {
			if errors.Is(insertErr, store.ErrDuplicateSignal) {
				dedupCount++
				continue
			}
			slog.Error("intercom_sync: insert signal failed",
				"conversation_id", conv.ID,
				"error", insertErr,
			)
			continue
		}
		insertedCount++
	}

	slog.Info("intercom_sync: signals inserted",
		"total_fetched", len(conversations),
		"total_inserted", insertedCount,
		"deduplicated", dedupCount,
	)

	// Stamp last_sync_at.
	if err := w.store.UpdateIntegrationLastSync(ctx, args.ProjectID, args.IntegrationID, time.Now().UTC()); err != nil {
		slog.Warn("intercom_sync: failed to update last_sync_at", "error", err)
	}

	// Chain embed job.
	if insertedCount > 0 {
		w.enqueueEmbed(ctx, args)
	}

	CompleteTask(ctx, w.store, args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, args.RunID)

	return nil
}

func (w *IntercomSyncWorker) enqueueEmbed(ctx context.Context, args IntercomSyncJobArgs) {
	client := w.jobCtx.Client()
	if client == nil {
		slog.Warn("intercom_sync: river client not available, skipping embed enqueue")
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
		slog.Warn("intercom_sync: failed to enqueue embed job", "error", err)
	}
}
