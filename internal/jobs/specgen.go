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

// SpecGenWorker generates a structured spec from a feature candidate.
type SpecGenWorker struct {
	river.WorkerDefaults[SpecGenJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewSpecGenWorker(s *store.Store, cfg *config.Config) *SpecGenWorker {
	return &SpecGenWorker{store: s, cfg: cfg}
}

func (w *SpecGenWorker) Work(ctx context.Context, job *river.Job[SpecGenJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("generating spec",
		"candidate_id", job.Args.CandidateID,
		"project_id", job.Args.ProjectID,
	)

	// Fetch candidate and its signals
	candidate, err := w.store.GetCandidateInternal(ctx, job.Args.CandidateID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	signals, err := w.store.GetCandidateSignals(ctx, job.Args.CandidateID, 20)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// Build signal context
	var signalContext string
	for i, sig := range signals {
		if i > 0 {
			signalContext += "\n---\n"
		}
		signalContext += fmt.Sprintf("[%s | %s] %s", sig.Source, sig.Type, sig.Content)
	}

	// Generate spec via Anthropic API
	llmStart := time.Now()
	spec, llmResp, err := generateSpecViaLLM(ctx, w.cfg.AnthropicAPIKey, candidate.Title, candidate.ProblemSummary, signalContext)
	llmLatency := trackDuration(llmStart)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, job.Args.ProjectID,
			ptrUUID(job.Args.RunID), ptrUUID(job.Args.TaskID),
			domain.LLMProviderAnthropic, "claude-sonnet-4-6-20250514",
			domain.LLMCallTypeSpecGen,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	spec.CandidateID = job.Args.CandidateID
	spec.ProjectID = job.Args.ProjectID

	createdSpec, err := w.store.CreateSpec(ctx, *spec)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}
	spec = &createdSpec

	// Update candidate status
	if _, err := w.store.UpdateCandidateStatus(ctx, job.Args.ProjectID, job.Args.CandidateID, domain.CandidateStatusSpecced); err != nil {
		slog.Error("failed to update candidate status", "error", err)
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, job.Args.RunID)

	// Trigger copilot spec review
	client := getRiverClient()
	if client != nil {
		_, err := client.Insert(ctx, CopilotReviewJobArgs{
			ProjectID:  job.Args.ProjectID,
			TargetType: "spec",
			TargetID:   spec.ID,
		}, &river.InsertOpts{Queue: "default"})
		if err != nil {
			slog.Error("failed to enqueue copilot spec review", "error", err)
		}
	}

	return nil
}

func generateSpecViaLLM(ctx context.Context, apiKey string, title, problemSummary, signalContext string) (*domain.Spec, *anthropicResponse, error) {
	if apiKey == "" {
		return &domain.Spec{
			ID:               uuid.New(),
			ProblemStatement: "No API key configured. " + problemSummary,
			ProposedSolution: "Configure ANTHROPIC_API_KEY to generate real specs.",
			UserStories:      []domain.UserStory{{Role: "user", Want: "use the feature", SoThat: "my problem is solved"}},
			AcceptanceCriteria: []string{"Feature works as described"},
			OutOfScope:       []string{"Not defined yet"},
			Version:          1,
		}, nil, nil
	}

	prompt := fmt.Sprintf(`You are a senior product manager generating a structured product spec.

Feature: %s
Problem: %s

Supporting customer signals:
%s

Generate a complete product spec in the following JSON format:
{
  "problem_statement": "What user pain this addresses, grounded in signal language",
  "proposed_solution": "High-level description of the change",
  "user_stories": [{"role": "...", "want": "...", "so_that": "..."}],
  "acceptance_criteria": ["Testable condition 1", "Testable condition 2"],
  "out_of_scope": ["Explicit exclusion 1"],
  "ui_changes": "Description of UI component changes",
  "data_model_changes": "Tables/fields to add or modify",
  "open_questions": ["Unresolved decision 1"]
}

Be specific, grounded in the actual customer signals, and pragmatic. Include 3-5 user stories, 4-6 acceptance criteria, and 2-3 open questions.`, title, problemSummary, signalContext)

	payload := map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": 2048,
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
		return nil, &result, fmt.Errorf("empty response from LLM")
	}

	// Parse the JSON response
	var specData struct {
		ProblemStatement   string             `json:"problem_statement"`
		ProposedSolution   string             `json:"proposed_solution"`
		UserStories        []domain.UserStory `json:"user_stories"`
		AcceptanceCriteria []string           `json:"acceptance_criteria"`
		OutOfScope         []string           `json:"out_of_scope"`
		UIChanges          string             `json:"ui_changes"`
		DataModelChanges   string             `json:"data_model_changes"`
		OpenQuestions      []string           `json:"open_questions"`
	}

	text := result.Content[0].Text
	// Try to extract JSON from the response (it might be wrapped in markdown code blocks)
	if idx := findJSONStart(text); idx >= 0 {
		text = text[idx:]
	}
	if idx := findJSONEnd(text); idx >= 0 {
		text = text[:idx+1]
	}

	if err := json.Unmarshal([]byte(text), &specData); err != nil {
		// If parsing fails, use the raw text as the problem statement
		return &domain.Spec{
			ID:               uuid.New(),
			ProblemStatement: result.Content[0].Text,
			Version:          1,
		}, &result, nil
	}

	return &domain.Spec{
		ID:                 uuid.New(),
		ProblemStatement:   specData.ProblemStatement,
		ProposedSolution:   specData.ProposedSolution,
		UserStories:        specData.UserStories,
		AcceptanceCriteria: specData.AcceptanceCriteria,
		OutOfScope:         specData.OutOfScope,
		UIChanges:          specData.UIChanges,
		DataModelChanges:   specData.DataModelChanges,
		OpenQuestions:       specData.OpenQuestions,
		Version:            1,
	}, &result, nil
}

func findJSONStart(s string) int {
	for i, c := range s {
		if c == '{' {
			return i
		}
	}
	return -1
}

func findJSONEnd(s string) int {
	depth := 0
	start := false
	for i, c := range s {
		switch c {
		case '{':
			depth++
			start = true
		case '}':
			depth--
			if start && depth == 0 {
				return i
			}
		}
	}
	return -1
}
