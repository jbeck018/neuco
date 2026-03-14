package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/ai"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// UpdateContextWorker generates project context insights from the synthesis run's
// candidates. It reads prior context, asks the LLM what's new/changed, and
// persists the resulting insights with embeddings for future similarity search.
type UpdateContextWorker struct {
	river.WorkerDefaults[UpdateContextJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewUpdateContextWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *UpdateContextWorker {
	return &UpdateContextWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}
func (w *UpdateContextWorker) Work(ctx context.Context, job *river.Job[UpdateContextJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("updating project context", "project_id", job.Args.ProjectID, "run_id", job.Args.RunID)

	// 1. Get candidates from this synthesis run.
	candidates, _, err := w.store.ListProjectCandidates(ctx, job.Args.ProjectID, store.Page(50, 0))
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		return err
	}

	if len(candidates) == 0 {
		slog.Info("no candidates to derive context from", "project_id", job.Args.ProjectID)
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		w.triggerCopilotReview(ctx, job.Args.ProjectID, job.Args.RunID)
		return nil
	}

	// 2. Get existing context for comparison.
	priorContexts, err := w.store.ListProjectContextsInternal(ctx, job.Args.ProjectID, 20)
	if err != nil {
		slog.Error("failed to fetch prior contexts", "error", err)
		// Non-fatal: proceed without prior context.
		priorContexts = nil
	}

	// 3. Build the LLM prompt.
	var candidateSummaries string
	for i, c := range candidates {
		if i > 0 {
			candidateSummaries += "\n"
		}
		candidateSummaries += fmt.Sprintf("- %s (score: %.1f, %d signals): %s",
			c.Title, c.Score, c.SignalCount, c.ProblemSummary)
	}

	var priorContextSummary string
	if len(priorContexts) > 0 {
		for _, pc := range priorContexts {
			priorContextSummary += fmt.Sprintf("- [%s] %s: %s\n", pc.Category, pc.Title, pc.Content)
		}
	}

	insights, llmResp, err := generateContextInsights(ctx, w.cfg.AnthropicAPIKey, candidateSummaries, priorContextSummary)
	llmLatency := trackDuration(start)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, job.Args.ProjectID,
			ptrUUID(job.Args.RunID), ptrUUID(job.Args.TaskID),
			domain.LLMProviderAnthropic, "claude-haiku-4-5-20251001",
			domain.LLMCallTypeContextUpdate,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		slog.Error("failed to generate context insights", "error", err)
		// Non-fatal: complete the pipeline even if context generation fails.
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		w.triggerCopilotReview(ctx, job.Args.ProjectID, job.Args.RunID)
		return nil
	}

	// 4. Store the new insights.
	llmClient := ai.NewLLMClient(w.cfg.AnthropicAPIKey, w.cfg.OpenAIAPIKey)
	runID := job.Args.RunID
	for _, insight := range insights {
		pc := domain.ProjectContext{
			ProjectID:   job.Args.ProjectID,
			Category:    domain.ContextCategory(insight.Category),
			Title:       insight.Title,
			Content:     insight.Content,
			SourceRunID: &runID,
		}

		inserted, err := w.store.InsertProjectContext(ctx, pc)
		if err != nil {
			slog.Error("failed to insert context", "title", insight.Title, "error", err)
			continue
		}

		// Generate and store embedding for similarity search.
		embedding, err := llmClient.GenerateEmbedding(ctx, insight.Title+" "+insight.Content)
		if err != nil {
			slog.Error("failed to generate context embedding", "id", inserted.ID, "error", err)
			continue
		}
		if err := w.store.UpdateProjectContextEmbedding(ctx, inserted.ID, embedding); err != nil {
			slog.Error("failed to store context embedding", "id", inserted.ID, "error", err)
		}
	}

	slog.Info("project context updated", "project_id", job.Args.ProjectID, "insights", len(insights))

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
	w.triggerCopilotReview(ctx, job.Args.ProjectID, job.Args.RunID)

	return nil
}

func (w *UpdateContextWorker) triggerCopilotReview(ctx context.Context, projectID, runID uuid.UUID) {
	client := w.jobCtx.Client()
	if client != nil {
		_, err := client.Insert(ctx, CopilotReviewJobArgs{
			ProjectID:  projectID,
			TargetType: "synthesis",
			TargetID:   runID,
		}, &river.InsertOpts{Queue: "default"})
		if err != nil {
			slog.Error("failed to enqueue copilot review", "error", err)
		}
	}
}

type contextInsight struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

func generateContextInsights(ctx context.Context, apiKey string, candidates string, priorContext string) ([]contextInsight, *anthropicResponse, error) {
	if apiKey == "" {
		return nil, nil, nil
	}

	priorSection := ""
	if priorContext != "" {
		priorSection = fmt.Sprintf(`
Prior project context (avoid duplicating these):
%s
`, priorContext)
	}

	prompt := fmt.Sprintf(`You are a product intelligence analyst. Given the following synthesis results (feature candidates found from customer signals), extract key insights that should be remembered for future analysis.
%s
Current synthesis results:
%s

Generate 2-5 insights. Each should be categorized as one of: insight, theme, decision, risk, opportunity.
Focus on cross-cutting patterns, emerging trends, and strategic observations that would be valuable context for future synthesis runs. Do NOT repeat existing context.

Respond in JSON: {"insights": [{"category": "...", "title": "...", "content": "..."}]}`, priorSection, candidates)

	payload := map[string]interface{}{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 1000,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}

	req, err := newHTTPRequest(ctx, "POST", "https://api.anthropic.com/v1/messages", body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := doHTTPRequest(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}

	if len(result.Content) == 0 {
		return nil, &result, nil
	}

	var parsed struct {
		Insights []contextInsight `json:"insights"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		return nil, &result, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return parsed.Insights, &result, nil
}
