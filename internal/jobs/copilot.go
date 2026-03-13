package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// CopilotReviewWorker generates AI co-pilot insights for various targets.
type CopilotReviewWorker struct {
	river.WorkerDefaults[CopilotReviewJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewCopilotReviewWorker(s *store.Store, cfg *config.Config) *CopilotReviewWorker {
	return &CopilotReviewWorker{store: s, cfg: cfg}
}

func (w *CopilotReviewWorker) Work(ctx context.Context, job *river.Job[CopilotReviewJobArgs]) error {
	start := time.Now()
	if job.Args.TaskID != uuid.Nil {
		StartTask(ctx, w.store, job.Args.TaskID)
	}

	slog.Info("copilot review",
		"target_type", job.Args.TargetType,
		"target_id", job.Args.TargetID,
		"project_id", job.Args.ProjectID,
	)

	var notes []domain.CopilotNote

	switch job.Args.TargetType {
	case "spec":
		var err error
		notes, err = w.reviewSpec(ctx, job.Args.ProjectID, job.Args.TargetID, job.Args.RunID, job.Args.TaskID)
		if err != nil {
			slog.Error("copilot spec review failed", "error", err)
			if job.Args.TaskID != uuid.Nil {
				FailTask(ctx, w.store, job.Args.TaskID, err)
			}
			return err
		}

	case "generation":
		var err error
		notes, err = w.reviewGeneration(ctx, job.Args.ProjectID, job.Args.TargetID, job.Args.RunID, job.Args.TaskID)
		if err != nil {
			slog.Error("copilot generation review failed", "error", err)
			if job.Args.TaskID != uuid.Nil {
				FailTask(ctx, w.store, job.Args.TaskID, err)
			}
			return err
		}

	case "synthesis":
		var err error
		notes, err = w.reviewSynthesis(ctx, job.Args.ProjectID, job.Args.TargetID, job.Args.RunID, job.Args.TaskID)
		if err != nil {
			slog.Error("copilot synthesis review failed", "error", err)
			if job.Args.TaskID != uuid.Nil {
				FailTask(ctx, w.store, job.Args.TaskID, err)
			}
			return err
		}

	default:
		slog.Warn("unknown copilot review target type", "type", job.Args.TargetType)
	}

	// Store all generated notes
	for _, note := range notes {
		if err := w.store.CreateCopilotNote(ctx, &note); err != nil {
			slog.Error("failed to store copilot note", "error", err)
		}
	}

	if job.Args.TaskID != uuid.Nil {
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
	}

	return nil
}

func (w *CopilotReviewWorker) reviewSpec(ctx context.Context, projectID, specID, pipelineRunID, pipelineTaskID uuid.UUID) ([]domain.CopilotNote, error) {
	spec, err := w.store.GetSpecInternal(ctx, specID)
	if err != nil {
		return nil, err
	}

	if w.cfg.AnthropicAPIKey == "" {
		return []domain.CopilotNote{
			{
				ID:         uuid.New(),
				ProjectID:  projectID,
				TargetType: "spec",
				TargetID:   specID,
				NoteType:   domain.CopilotNoteTypeInsight,
				Content:    "Configure ANTHROPIC_API_KEY to enable AI-powered spec reviews.",
			},
		}, nil
	}

	userStoriesJSON, _ := json.Marshal(spec.UserStories)
	acJSON, _ := json.Marshal(spec.AcceptanceCriteria)
	oosJSON, _ := json.Marshal(spec.OutOfScope)
	oqJSON, _ := json.Marshal(spec.OpenQuestions)

	prompt := fmt.Sprintf(`You are a senior product advisor reviewing a feature spec. Provide actionable, specific feedback.

## Spec
- Problem: %s
- Solution: %s
- User Stories: %s
- Acceptance Criteria: %s
- Out of Scope: %s
- UI Changes: %s
- Data Model Changes: %s
- Open Questions: %s

Review this spec and provide feedback as a JSON array of notes:
[
  {"type": "risk", "content": "specific risk description"},
  {"type": "suggestion", "content": "specific improvement"},
  {"type": "review", "content": "overall assessment point"}
]

Types:
- "risk": potential problems, edge cases, or scalability concerns
- "suggestion": concrete improvements or missing elements
- "review": general observations about quality or completeness

Provide 3-5 notes. Be specific and actionable, not generic.`, spec.ProblemStatement, spec.ProposedSolution, string(userStoriesJSON), string(acJSON), string(oosJSON), spec.UIChanges, spec.DataModelChanges, string(oqJSON))

	llmStart := time.Now()
	notes, llmResp, err := callCopilotLLM(ctx, w.cfg.AnthropicAPIKey, prompt)
	llmLatency := trackDuration(llmStart)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, projectID,
			ptrUUID(pipelineRunID), ptrUUID(pipelineTaskID),
			domain.LLMProviderAnthropic, "claude-haiku-4-5-20251001",
			domain.LLMCallTypeCopilotReview,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		return nil, err
	}

	var result []domain.CopilotNote
	for _, n := range notes {
		result = append(result, domain.CopilotNote{
			ID:         uuid.New(),
			ProjectID:  projectID,
			TargetType: "spec",
			TargetID:   specID,
			NoteType:   domain.CopilotNoteType(n.Type),
			Content:    n.Content,
		})
	}

	return result, nil
}

func (w *CopilotReviewWorker) reviewGeneration(ctx context.Context, projectID, generationID, pipelineRunID, pipelineTaskID uuid.UUID) ([]domain.CopilotNote, error) {
	gen, err := w.store.GetGeneration(ctx, generationID)
	if err != nil {
		return nil, err
	}

	if w.cfg.AnthropicAPIKey == "" || len(gen.Files) == 0 {
		return nil, nil
	}

	// Build file context
	var fileContext string
	for _, f := range gen.Files {
		fileContext += fmt.Sprintf("\n### %s\n```\n%s\n```\n", f.Path, f.Content)
	}

	prompt := fmt.Sprintf(`You are a senior frontend engineer reviewing generated code. Provide specific, actionable feedback.

## Generated Files
%s

Review this code and provide feedback as a JSON array:
[
  {"type": "risk", "content": "specific issue"},
  {"type": "suggestion", "content": "specific improvement"}
]

Focus on:
- Missing error/loading/empty states
- Accessibility issues (ARIA labels, keyboard nav)
- TypeScript type safety
- Component composition patterns
- Performance concerns (unnecessary re-renders, missing memoization)

Provide 3-5 notes. Be specific — reference exact components or patterns.`, fileContext)

	llmStart := time.Now()
	notes, llmResp, err := callCopilotLLM(ctx, w.cfg.AnthropicAPIKey, prompt)
	llmLatency := trackDuration(llmStart)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, projectID,
			ptrUUID(pipelineRunID), ptrUUID(pipelineTaskID),
			domain.LLMProviderAnthropic, "claude-haiku-4-5-20251001",
			domain.LLMCallTypeCopilotReview,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		return nil, err
	}

	var result []domain.CopilotNote
	for _, n := range notes {
		result = append(result, domain.CopilotNote{
			ID:         uuid.New(),
			ProjectID:  projectID,
			TargetType: "generation",
			TargetID:   generationID,
			NoteType:   domain.CopilotNoteType(n.Type),
			Content:    n.Content,
		})
	}

	return result, nil
}

func (w *CopilotReviewWorker) reviewSynthesis(ctx context.Context, projectID, runID, pipelineRunID, pipelineTaskID uuid.UUID) ([]domain.CopilotNote, error) {
	candidates, _, err := w.store.ListProjectCandidates(ctx, projectID, store.PageParams{Limit: 10, Offset: 0})
	if err != nil {
		return nil, err
	}

	if w.cfg.AnthropicAPIKey == "" || len(candidates) == 0 {
		return nil, nil
	}

	var candidateContext string
	for i, c := range candidates {
		candidateContext += fmt.Sprintf("%d. **%s** (score: %.2f, %d signals)\n   %s\n\n",
			i+1, c.Title, c.Score, c.SignalCount, c.ProblemSummary)
	}

	prompt := fmt.Sprintf(`You are a strategic product advisor reviewing synthesis results. The system analyzed customer signals and identified these feature candidates:

%s

Provide strategic insights as a JSON array:
[
  {"type": "insight", "content": "strategic observation"},
  {"type": "suggestion", "content": "recommendation"}
]

Focus on:
- Patterns across themes (e.g., "3 of 5 themes relate to onboarding")
- Potential epic groupings
- Risk assessment (are high-score items also high-effort?)
- Gaps (important customer segments underrepresented?)

Provide 2-4 notes. Be strategic, not tactical.`, candidateContext)

	llmStart := time.Now()
	notes, llmResp, err := callCopilotLLM(ctx, w.cfg.AnthropicAPIKey, prompt)
	llmLatency := trackDuration(llmStart)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, projectID,
			ptrUUID(pipelineRunID), ptrUUID(pipelineTaskID),
			domain.LLMProviderAnthropic, "claude-haiku-4-5-20251001",
			domain.LLMCallTypeCopilotReview,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		return nil, err
	}

	var result []domain.CopilotNote
	for _, n := range notes {
		result = append(result, domain.CopilotNote{
			ID:         uuid.New(),
			ProjectID:  projectID,
			TargetType: "synthesis",
			TargetID:   runID,
			NoteType:   domain.CopilotNoteType(n.Type),
			Content:    n.Content,
		})
	}

	return result, nil
}

type copilotLLMNote struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

func callCopilotLLM(ctx context.Context, apiKey, prompt string) ([]copilotLLMNote, *anthropicResponse, error) {
	payload := map[string]interface{}{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 1024,
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
		return nil, &result, fmt.Errorf("empty copilot response")
	}

	text := result.Content[0].Text
	if idx := findJSONArrayStart(text); idx >= 0 {
		text = text[idx:]
	}
	if idx := findJSONArrayEnd(text); idx >= 0 {
		text = text[:idx+1]
	}

	var notes []copilotLLMNote
	if err := json.Unmarshal([]byte(text), &notes); err != nil {
		return []copilotLLMNote{
			{Type: "insight", Content: result.Content[0].Text},
		}, &result, nil
	}

	return notes, &result, nil
}
