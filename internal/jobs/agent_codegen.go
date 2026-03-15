package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// CreateAgentCodegenPipeline creates and starts the agent-based codegen pipeline.
func CreateAgentCodegenPipeline(ctx context.Context, s *store.Store, client *river.Client[pgx.Tx], specID, projectID, generationID uuid.UUID) (uuid.UUID, error) {
	metadata := map[string]string{
		"spec_id":       specID.String(),
		"generation_id": generationID.String(),
	}

	run, err := s.CreatePipelineRun(ctx, projectID, domain.PipelineTypeCodegen, metadata)
	if err != nil {
		return uuid.Nil, err
	}

	taskNames := []string{"prepare_context", "provision_sandbox", "run_agent", "validate_output", "create_pr", "notify"}
	taskIDs := make(map[string]uuid.UUID, len(taskNames))
	for i, name := range taskNames {
		task, err := s.CreatePipelineTask(ctx, run.ID, name, i)
		if err != nil {
			return uuid.Nil, err
		}
		taskIDs[name] = task.ID
	}

	if client == nil {
		return uuid.Nil, fmt.Errorf("create agent codegen pipeline: river client is nil")
	}

	_, err = client.Insert(ctx, PrepareContextJobArgs{
		SpecID:       specID,
		ProjectID:    projectID,
		GenerationID: generationID,
		RunID:        run.ID,
		TaskID:       taskIDs["prepare_context"],
	}, &river.InsertOpts{Queue: "codegen"})
	if err != nil {
		return uuid.Nil, err
	}

	return run.ID, nil
}

// PrepareContextWorker prepares rich context payload for agent execution.
type PrepareContextWorker struct {
	river.WorkerDefaults[PrepareContextJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewPrepareContextWorker(s *store.Store, jobCtx *JobContext) *PrepareContextWorker {
	return &PrepareContextWorker{store: s, jobCtx: jobCtx}
}

func (w *PrepareContextWorker) Work(ctx context.Context, job *river.Job[PrepareContextJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	spec, err := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	project, err := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// TODO(agent-codegen): Build rich repository context via BuildRichContext.
	contextPayload, err := json.Marshal(map[string]any{
		"spec_id":            spec.ID,
		"project_id":         project.ID,
		"problem_statement":  spec.ProblemStatement,
		"proposed_solution":  spec.ProposedSolution,
		"acceptance_criteria": spec.AcceptanceCriteria,
	})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	client := w.jobCtx.Client()
	if client != nil {
		nextTaskID := pipelineTaskIDByName(ctx, w.store, job.Args.RunID, "provision_sandbox")
		_, err := client.Insert(ctx, ProvisionSandboxJobArgs{
			SpecID:         job.Args.SpecID,
			ProjectID:      job.Args.ProjectID,
			GenerationID:   job.Args.GenerationID,
			RunID:          job.Args.RunID,
			TaskID:         nextTaskID,
			ContextPayload: contextPayload,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain provision_sandbox job", "error", err)
		}
	}

	return nil
}

// ProvisionSandboxWorker provisions a sandbox and writes generation instruction files.
type ProvisionSandboxWorker struct {
	river.WorkerDefaults[ProvisionSandboxJobArgs]
	store          *store.Store
	cfg            *config.Config
	jobCtx         *JobContext
	sandboxManager codegen.SandboxManager
}

func NewProvisionSandboxWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *ProvisionSandboxWorker {
	manager, err := codegen.NewSandboxManager(cfg.SandboxProvider, cfg)
	if err != nil {
		slog.Error("failed to initialize sandbox manager", "provider", cfg.SandboxProvider, "error", err)
	}

	return &ProvisionSandboxWorker{store: s, cfg: cfg, jobCtx: jobCtx, sandboxManager: manager}
}

func (w *ProvisionSandboxWorker) Work(ctx context.Context, job *river.Job[ProvisionSandboxJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	spec, err := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	project, err := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	if w.sandboxManager == nil {
		err := fmt.Errorf("sandbox manager is not configured")
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	repoURL := deriveRepoURL(project.GitHubRepo)
	if strings.TrimSpace(repoURL) == "" {
		err := fmt.Errorf("project has no github repo configured")
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	timeoutSeconds := w.cfg.SandboxTimeoutMinutes * 60
	sb, err := w.sandboxManager.Provision(ctx, codegen.SandboxConfig{
		GenerationID:   job.Args.GenerationID.String(),
		RepoURL:        repoURL,
		TimeoutSeconds: timeoutSeconds,
	})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// TODO(agent-codegen): Replace with BuildInstructions/BuildConventions + curated context files.
	instructions := fmt.Sprintf("# Agent Code Generation\n\nImplement spec %s for project %s.\n", spec.ID, project.Name)
	conventions := "# Project Conventions\n\nFollow existing repository patterns and conventions.\n"
	contextJSON := "{}"
	if len(job.Args.ContextPayload) > 0 {
		contextJSON = string(job.Args.ContextPayload)
	}

	err = w.sandboxManager.WriteFiles(ctx, sb, map[string]string{
		"INSTRUCTIONS.md":              instructions,
		"CONVENTIONS.md":              conventions,
		".neuco/context/context.json": contextJSON,
	})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	effectiveCfg, err := w.store.GetEffectiveConfig(ctx, project.OrgID, project.ID)
	if err != nil {
		slog.Warn("failed to load effective agent config", "project_id", project.ID, "error", err)
	}

	agentProvider := "claude-code"
	var agentModel *string
	if effectiveCfg != nil {
		if strings.TrimSpace(effectiveCfg.Provider) != "" {
			agentProvider = effectiveCfg.Provider
		}
		agentModel = effectiveCfg.ModelOverride
	}

	externalID := sb.ID
	session, err := w.store.CreateSandboxSession(ctx, store.SandboxSessionRow{
		GenerationID:      job.Args.GenerationID,
		ProjectID:         project.ID,
		OrgID:             project.OrgID,
		AgentProvider:     agentProvider,
		AgentModel:        agentModel,
		SandboxProvider:   sb.Provider,
		SandboxExternalID: &externalID,
		Status:            "provisioned",
		RetryCount:        0,
		MaxRetries:        normalizeMaxRetries(w.cfg.SandboxMaxRetries),
	})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	client := w.jobCtx.Client()
	if client != nil {
		nextTaskID := pipelineTaskIDByName(ctx, w.store, job.Args.RunID, "run_agent")
		_, err := client.Insert(ctx, RunAgentJobArgs{
			SpecID:       job.Args.SpecID,
			ProjectID:    job.Args.ProjectID,
			GenerationID: job.Args.GenerationID,
			RunID:        job.Args.RunID,
			TaskID:       nextTaskID,
			SandboxID:    sb.ID,
			SessionID:    session.ID,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain run_agent job", "error", err)
		}
	}

	return nil
}

// RunAgentWorker executes the configured coding agent in the sandbox.
type RunAgentWorker struct {
	river.WorkerDefaults[RunAgentJobArgs]
	store            *store.Store
	cfg              *config.Config
	jobCtx           *JobContext
	providerRegistry *codegen.ProviderRegistry
}

func NewRunAgentWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *RunAgentWorker {
	registry := codegen.NewProviderRegistry(codegen.ClaudeCodeProvider{})
	return &RunAgentWorker{store: s, cfg: cfg, jobCtx: jobCtx, providerRegistry: registry}
}

func (w *RunAgentWorker) Work(ctx context.Context, job *river.Job[RunAgentJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	spec, err := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	project, err := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	session, err := w.store.GetSandboxSession(ctx, job.Args.SessionID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	if err := w.store.UpdateSandboxSessionStatus(ctx, session.ID, "running", nil); err != nil {
		slog.Warn("failed to mark sandbox session running", "session_id", session.ID, "error", err)
	}

	effectiveCfg, err := w.store.GetEffectiveConfig(ctx, project.OrgID, project.ID)
	if err != nil {
		slog.Warn("failed to load effective agent config", "project_id", project.ID, "error", err)
	}

	providerName := session.AgentProvider
	if effectiveCfg != nil && strings.TrimSpace(effectiveCfg.Provider) != "" {
		providerName = effectiveCfg.Provider
	}
	if strings.TrimSpace(providerName) == "" {
		providerName = "claude-code"
	}

	provider, ok := w.providerRegistry.Get(providerName)
	if !ok {
		err := fmt.Errorf("agent provider %q is not registered", providerName)
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	extraConfig := map[string]string{}
	if effectiveCfg != nil {
		extraConfig = decodeAgentExtraConfig(effectiveCfg.ExtraConfig)
	}

	apiKey := strings.TrimSpace(extraConfig["ANTHROPIC_API_KEY"])
	if apiKey == "" && effectiveCfg != nil && len(effectiveCfg.EncryptedAPIKey) > 0 {
		key, derr := codegen.DeriveKey(w.cfg.EncryptionKey)
		if derr == nil {
			plaintext, decryptErr := codegen.Decrypt(effectiveCfg.EncryptedAPIKey, key)
			if decryptErr == nil {
				apiKey = strings.TrimSpace(string(plaintext))
			}
		}
	}

	_ = provider
	_ = apiKey
	_ = spec
	// TODO(agent-codegen): Build command from provider, stream output to sandbox session logs, and execute in sandbox.
	logLine := fmt.Sprintf("run_agent placeholder executed for generation=%s sandbox=%s", job.Args.GenerationID, job.Args.SandboxID)
	if err := w.store.UpdateSandboxSessionResult(ctx, session.ID, store.SandboxSessionResult{AgentLog: &logLine}); err != nil {
		slog.Warn("failed to write sandbox session log", "session_id", session.ID, "error", err)
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	client := w.jobCtx.Client()
	if client != nil {
		nextTaskID := pipelineTaskIDByName(ctx, w.store, job.Args.RunID, "validate_output")
		_, err := client.Insert(ctx, ValidateOutputJobArgs{
			SpecID:       job.Args.SpecID,
			ProjectID:    job.Args.ProjectID,
			GenerationID: job.Args.GenerationID,
			RunID:        job.Args.RunID,
			TaskID:       nextTaskID,
			SandboxID:    job.Args.SandboxID,
			SessionID:    job.Args.SessionID,
			RetryCount:   session.RetryCount,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain validate_output job", "error", err)
		}
	}
	return nil
}

// ValidateOutputWorker validates generated output and controls retry flow.
type ValidateOutputWorker struct {
	river.WorkerDefaults[ValidateOutputJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewValidateOutputWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *ValidateOutputWorker {
	return &ValidateOutputWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}

func (w *ValidateOutputWorker) Work(ctx context.Context, job *river.Job[ValidateOutputJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	_, err := w.store.GetSandboxSession(ctx, job.Args.SessionID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// TODO(agent-codegen): Execute validation commands (test/lint/typecheck) inside the sandbox.
	validationPassed := true
	maxRetries := normalizeMaxRetries(w.cfg.SandboxMaxRetries)

	if !validationPassed {
		if job.Args.RetryCount < maxRetries {
			nextRetry := job.Args.RetryCount + 1
			if err := w.store.UpdateSandboxSessionResult(ctx, job.Args.SessionID, store.SandboxSessionResult{RetryCount: &nextRetry}); err != nil {
				slog.Warn("failed to increment sandbox session retry count", "session_id", job.Args.SessionID, "error", err)
			}

			CompleteTask(ctx, w.store, job.Args.TaskID, start)

			client := w.jobCtx.Client()
			if client != nil {
				nextTaskID := pipelineTaskIDByName(ctx, w.store, job.Args.RunID, "run_agent")
				_, insertErr := client.Insert(ctx, RunAgentJobArgs{
					SpecID:       job.Args.SpecID,
					ProjectID:    job.Args.ProjectID,
					GenerationID: job.Args.GenerationID,
					RunID:        job.Args.RunID,
					TaskID:       nextTaskID,
					SandboxID:    job.Args.SandboxID,
					SessionID:    job.Args.SessionID,
				}, &river.InsertOpts{Queue: "codegen"})
				if insertErr != nil {
					slog.Error("failed to requeue run_agent job", "error", insertErr)
				}
			}
			return nil
		}
		gen, genErr := w.store.GetGeneration(ctx, job.Args.GenerationID)
		if genErr == nil {
			gen.Status = domain.GenerationStatusFailed
			gen.ErrorMsg = "validation failed after maximum retries"
			now := time.Now()
			gen.CompletedAt = &now
			_ = w.store.UpdateGeneration(ctx, gen)
		}

		err := fmt.Errorf("validation failed after maximum retries")
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// TODO(agent-codegen): Collect sandbox diff and persist generated files before PR creation.
	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	client := w.jobCtx.Client()
	if client != nil {
		nextTaskID := pipelineTaskIDByName(ctx, w.store, job.Args.RunID, "create_pr")
		_, err := client.Insert(ctx, CreatePRJobArgs{
			SpecID:       job.Args.SpecID,
			ProjectID:    job.Args.ProjectID,
			GenerationID: job.Args.GenerationID,
			RunID:        job.Args.RunID,
			TaskID:       nextTaskID,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain create_pr job", "error", err)
		}
	}

	return nil
}

func pipelineTaskIDByName(ctx context.Context, s *store.Store, runID uuid.UUID, taskName string) uuid.UUID {
	run, err := s.GetPipelineRun(ctx, runID)
	if err != nil || run == nil {
		return uuid.Nil
	}
	for _, task := range run.Tasks {
		if task.Name == taskName {
			return task.ID
		}
	}
	return uuid.Nil
}

func deriveRepoURL(repo string) string {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "git@") {
		return trimmed
	}
	base := strings.TrimSuffix(trimmed, ".git")
	if strings.Contains(base, "/") {
		return "https://github.com/" + base + ".git"
	}
	return trimmed
}

func normalizeMaxRetries(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

func decodeAgentExtraConfig(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]string{}
	}
	if out == nil {
		return map[string]string{}
	}
	return out
}
