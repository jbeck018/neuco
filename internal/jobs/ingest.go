package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/ai"
	"github.com/neuco-ai/neuco/internal/ai/agents"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// longFormThreshold is the minimum character count that triggers the
// TranscriptAgent path instead of the basic single-signal ingest path.
const longFormThreshold = 2000

// IngestWorker processes raw signal payloads into structured signals.
// For long-form content (> longFormThreshold chars) it delegates to the
// TranscriptAgent which extracts multiple discrete signals via a ReAct loop.
// Short content is stored as a single signal without LLM processing.
type IngestWorker struct {
	river.WorkerDefaults[IngestJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewIngestWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *IngestWorker {
	return &IngestWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}
func (w *IngestWorker) Work(ctx context.Context, job *river.Job[IngestJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("ingesting signal",
		"project_id", job.Args.ProjectID,
		"source", job.Args.Source,
	)

	// Parse the raw payload into signal(s)
	var rawSignal struct {
		Content    string          `json:"content"`
		Type       string          `json:"type"`
		SourceRef  string          `json:"source_ref"`
		Metadata   json.RawMessage `json:"metadata"`
		OccurredAt *time.Time      `json:"occurred_at"`
	}

	if err := json.Unmarshal(job.Args.RawPayload, &rawSignal); err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	occurredAt := time.Now()
	if rawSignal.OccurredAt != nil {
		occurredAt = *rawSignal.OccurredAt
	}

	_ = occurredAt // used in the short path below

	// Long-form content: run the TranscriptAgent to extract multiple signals.
	if len(rawSignal.Content) > longFormThreshold {
		return w.processLongForm(ctx, job, rawSignal.Content, start)
	}

	// Short-form content: basic single-signal ingest (original behaviour).
	return w.processShortForm(ctx, job, rawSignal, start)
}

// processLongForm delegates to TranscriptAgent and then enqueues embedding for
// each extracted signal.
func (w *IngestWorker) processLongForm(
	ctx context.Context,
	job *river.Job[IngestJobArgs],
	content string,
	start time.Time,
) error {
	slog.Info("ingest: long-form content detected, using TranscriptAgent",
		"project_id", job.Args.ProjectID,
		"content_len", len(content),
	)

	llm := ai.NewLLMClient(w.cfg.AnthropicAPIKey, w.cfg.OpenAIAPIKey)
	agent := agents.NewTranscriptAgent(llm, w.store)

	extracted, err := agent.Process(ctx, job.Args.ProjectID, content)
	if err != nil {
		slog.Error("ingest: TranscriptAgent error", "error", err, "project_id", job.Args.ProjectID)
	}

	slog.Info("ingest: TranscriptAgent complete",
		"project_id", job.Args.ProjectID,
		"signals_extracted", len(extracted),
	)

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	if len(extracted) == 0 {
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return nil
	}

	signalIDs := make([]uuid.UUID, len(extracted))
	for i, s := range extracted {
		signalIDs[i] = s.ID
	}

	client := w.jobCtx.Client()
	if client != nil {
		run, runErr := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var embedTaskID uuid.UUID
		if runErr == nil {
			for _, t := range run.Tasks {
				if t.Name == "embed" {
					embedTaskID = t.ID
					break
				}
			}
		}

		if _, insertErr := client.Insert(ctx, EmbedJobArgs{
			ProjectID: job.Args.ProjectID,
			SignalIDs: signalIDs,
			RunID:     job.Args.RunID,
			TaskID:    embedTaskID,
		}, &river.InsertOpts{Queue: "ingest"}); insertErr != nil {
			slog.Error("ingest: failed to chain embed job", "error", insertErr)
		}
	}

	return nil
}

// processShortForm is the original single-signal ingest path for content
// below the longFormThreshold.
func (w *IngestWorker) processShortForm(
	ctx context.Context,
	job *river.Job[IngestJobArgs],
	rawSignal struct {
		Content    string          `json:"content"`
		Type       string          `json:"type"`
		SourceRef  string          `json:"source_ref"`
		Metadata   json.RawMessage `json:"metadata"`
		OccurredAt *time.Time      `json:"occurred_at"`
	},
	start time.Time,
) error {
	signalType := rawSignal.Type
	if signalType == "" {
		signalType = string(domain.SignalTypeNote)
	}

	metadata := rawSignal.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}

	occurredAt := time.Now()
	if rawSignal.OccurredAt != nil {
		occurredAt = *rawSignal.OccurredAt
	}

	sig := domain.Signal{
		ID:         uuid.New(),
		ProjectID:  job.Args.ProjectID,
		Source:     domain.SignalSource(job.Args.Source),
		SourceRef:  rawSignal.SourceRef,
		Type:       domain.SignalType(signalType),
		Content:    rawSignal.Content,
		Metadata:   metadata,
		OccurredAt: occurredAt,
	}

	inserted, err := w.store.InsertSignal(ctx, sig)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateSignal) {
			slog.Info("ingest: exact duplicate detected, skipping",
				"project_id", job.Args.ProjectID,
				"source_ref", rawSignal.SourceRef,
			)
			CompleteTask(ctx, w.store, job.Args.TaskID, start)
			CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
			return nil
		}
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}
	signal := inserted

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	client := w.jobCtx.Client()
	if client != nil {
		run, err := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var embedTaskID uuid.UUID
		if err == nil {
			for _, t := range run.Tasks {
				if t.Name == "embed" {
					embedTaskID = t.ID
					break
				}
			}
		}

		_, err = client.Insert(ctx, EmbedJobArgs{
			ProjectID: job.Args.ProjectID,
			SignalIDs: []uuid.UUID{signal.ID},
			RunID:     job.Args.RunID,
			TaskID:    embedTaskID,
		}, &river.InsertOpts{Queue: "ingest"})
		if err != nil {
			slog.Error("failed to chain embed job", "error", err)
		}
	}

	return nil
}
// EmbedWorker generates embeddings for signals that don't have them.
type EmbedWorker struct {
	river.WorkerDefaults[EmbedJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewEmbedWorker(s *store.Store, cfg *config.Config) *EmbedWorker {
	return &EmbedWorker{store: s, cfg: cfg}
}

func (w *EmbedWorker) Work(ctx context.Context, job *river.Job[EmbedJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("embedding signals",
		"project_id", job.Args.ProjectID,
		"count", len(job.Args.SignalIDs),
	)

	// Fetch signals that need embedding
	var signalIDs []uuid.UUID
	if len(job.Args.SignalIDs) > 0 {
		signalIDs = job.Args.SignalIDs
	} else {
		// If no specific IDs, embed all unembedded signals for the project
		unembedded, err := w.store.ListUnembeddedSignals(ctx, job.Args.ProjectID, 100)
		if err != nil {
			FailTask(ctx, w.store, job.Args.TaskID, err)
			CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
			return err
		}
		for _, s := range unembedded {
			signalIDs = append(signalIDs, s.ID)
		}
	}

	if len(signalIDs) == 0 {
		slog.Info("no signals to embed")
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return nil
	}

	// Get signal contents for embedding
	for _, sigID := range signalIDs {
		sig, err := w.store.GetSignalInternal(ctx, sigID)
		if err != nil {
			slog.Error("failed to get signal for embedding", "signal_id", sigID, "error", err)
			continue
		}

		// Generate embedding via OpenAI API
		embedStart := time.Now()
		embedding, embedTokens, err := generateEmbedding(ctx, w.cfg.OpenAIAPIKey, sig.Content)
		embedLatency := trackDuration(embedStart)
		{
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			recordLLMCall(ctx, w.store, job.Args.ProjectID,
				ptrUUID(job.Args.RunID), ptrUUID(job.Args.TaskID),
				domain.LLMProviderOpenAI, "text-embedding-3-small",
				domain.LLMCallTypeEmbedding,
				embedTokens, 0, embedLatency,
				errMsg)
		}
		if err != nil {
			slog.Error("failed to generate embedding", "signal_id", sigID, "error", err)
			continue
		}

		if err := w.store.UpdateSignalEmbedding(ctx, sigID, embedding); err != nil {
			slog.Error("failed to store embedding", "signal_id", sigID, "error", err)
			continue
		}

		// Near-duplicate check: if cosine similarity >= 0.95 with an existing
		// signal, mark this one as a duplicate.
		if originalID, err := w.store.FindNearDuplicateSignal(ctx, job.Args.ProjectID, sigID, embedding, 0.95); err == nil && originalID != nil {
			if markErr := w.store.MarkAsDuplicate(ctx, sigID, *originalID); markErr != nil {
				slog.Error("failed to mark near-duplicate", "signal_id", sigID, "original_id", *originalID, "error", markErr)
			} else {
				slog.Info("embed: near-duplicate detected",
					"signal_id", sigID,
					"original_id", *originalID,
					"project_id", job.Args.ProjectID,
				)
			}
		}
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
	return nil
}

// generateEmbedding calls the OpenAI embeddings API.
// generateEmbedding calls the OpenAI embeddings API. Returns the embedding
// vector, the number of prompt tokens used, and any error.
func generateEmbedding(ctx context.Context, apiKey string, text string) ([]float32, int, error) {
	if apiKey == "" {
		slog.Warn("no OpenAI API key configured, returning zero embedding")
		embedding := make([]float32, 1536)
		return embedding, 0, nil
	}

	payload := map[string]interface{}{
		"model": "text-embedding-3-small",
		"input": text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}

	req, err := newHTTPRequest(ctx, "POST", "https://api.openai.com/v1/embeddings", body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := doHTTPRequest(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}
	if len(result.Data) == 0 {
		return nil, result.Usage.PromptTokens, nil
	}

	return result.Data[0].Embedding, result.Usage.PromptTokens, nil
}
