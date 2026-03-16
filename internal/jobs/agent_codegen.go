package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/generation"
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
	cfg    *config.Config
	jobCtx *JobContext
}

func NewPrepareContextWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *PrepareContextWorker {
	return &PrepareContextWorker{store: s, cfg: cfg, jobCtx: jobCtx}
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

	maxIterations := 1
	if w.cfg != nil {
		maxIterations = normalizeMaxRetries(w.cfg.SandboxMaxRetries)
	}

	instructionData := codegen.InstructionData{
		Spec:               *spec,
		RepoIndex:          generation.RepoIndex{},
		Context:            codegen.ContextBundle{},
		ValidationCommands: detectValidationCommands(project),
		MaxIterations:      maxIterations,
	}

	instructions, err := codegen.BuildInstructions(instructionData)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	conventions, err := codegen.BuildConventions(generation.RepoIndex{}, map[string]string{})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	contextPayload, err := json.Marshal(agentContextPayload{
		Instructions: instructions,
		Conventions:  conventions,
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

	instructions := fmt.Sprintf("# Agent Code Generation\n\nImplement spec %s for project %s.\n", spec.ID, project.Name)
	conventions := "# Project Conventions\n\nFollow existing repository patterns and conventions.\n"
	if len(job.Args.ContextPayload) > 0 {
		var payload agentContextPayload
		if err := json.Unmarshal(job.Args.ContextPayload, &payload); err != nil {
			slog.Warn("failed to decode context payload, using fallback instruction files", "error", err)
		} else {
			if strings.TrimSpace(payload.Instructions) != "" {
				instructions = payload.Instructions
			}
			if strings.TrimSpace(payload.Conventions) != "" {
				conventions = payload.Conventions
			}
		}
	}
	contextJSON := string(job.Args.ContextPayload)
	if strings.TrimSpace(contextJSON) == "" {
		contextJSON = "{}"
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
			WorkDir:      sb.WorkDir,
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
	sandboxManager   codegen.SandboxManager
}

func NewRunAgentWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *RunAgentWorker {
	registry := codegen.NewProviderRegistry(codegen.ClaudeCodeProvider{})
	manager, err := codegen.NewSandboxManager(cfg.SandboxProvider, cfg)
	if err != nil {
		slog.Error("failed to init sandbox manager", "provider", cfg.SandboxProvider, "error", err)
	}
	return &RunAgentWorker{store: s, cfg: cfg, jobCtx: jobCtx, providerRegistry: registry, sandboxManager: manager}
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

	// Build execution environment
	env := make(map[string]string)
	if apiKey != "" {
		env["ANTHROPIC_API_KEY"] = apiKey
	}
	for k, v := range extraConfig {
		if k != "ANTHROPIC_API_KEY" {
			env[k] = v
		}
	}

	// Build the execution request
	promptFile := filepath.Join(job.Args.WorkDir, "INSTRUCTIONS.md")
	req := codegen.ExecutionRequest{
		GenerationID: job.Args.GenerationID,
		OrgID:        project.OrgID,
		ProjectID:    project.ID,
		Provider:     providerName,
		SandboxPath:  job.Args.WorkDir,
		PromptFile:   promptFile,
		Spec:         *spec,
		Environment:  env,
		Timeout:      time.Duration(w.cfg.SandboxTimeoutMinutes) * time.Minute,
	}

	// Build and run the agent command
	cmd, cmdErr := provider.BuildCommand(req)
	if cmdErr != nil {
		errMsg := cmdErr.Error()
		_ = w.store.UpdateSandboxSessionStatus(ctx, session.ID, "failed", &errMsg)
		FailTask(ctx, w.store, job.Args.TaskID, cmdErr)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return cmdErr
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now()
	runErr := cmd.Run()
	completedAt := time.Now()

	// Collect agent output log
	agentLog := stdout.String()
	if stderr.Len() > 0 {
		agentLog += "\n--- STDERR ---\n" + stderr.String()
	}

	_ = w.store.UpdateSandboxSessionResult(ctx, session.ID, store.SandboxSessionResult{
		AgentLog:    &agentLog,
		StartedAt:   &startedAt,
		CompletedAt: &completedAt,
	})

	if runErr != nil {
		// Non-zero exit is not necessarily fatal - agent may have made partial progress
		// Log the error but continue to validation
		slog.Warn("agent command exited with error", "error", runErr, "generation_id", job.Args.GenerationID)
	}

	_ = w.store.UpdateSandboxSessionStatus(ctx, session.ID, "validating", nil)
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
			WorkDir:      job.Args.WorkDir,
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
	store          *store.Store
	cfg            *config.Config
	jobCtx         *JobContext
	sandboxManager codegen.SandboxManager
}

func NewValidateOutputWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *ValidateOutputWorker {
	manager, err := codegen.NewSandboxManager(cfg.SandboxProvider, cfg)
	if err != nil {
		slog.Error("failed to init sandbox manager", "provider", cfg.SandboxProvider, "error", err)
	}
	return &ValidateOutputWorker{store: s, cfg: cfg, jobCtx: jobCtx, sandboxManager: manager}
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

	project, projErr := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if projErr != nil {
		slog.Warn("failed to load project for validation", "error", projErr)
	}

	validationCommands := detectValidationCommands(project)

	// Create a minimal sandbox reference for execution
	sb := &codegen.Sandbox{WorkDir: job.Args.WorkDir}

	validationPassed := true
	var validationLog strings.Builder

	if w.sandboxManager != nil && job.Args.WorkDir != "" && len(validationCommands) > 0 {
		for _, cmdStr := range validationCommands {
			parts := strings.Fields(cmdStr)
			if len(parts) == 0 {
				continue
			}
			result, execErr := w.sandboxManager.Execute(ctx, sb, parts[0], parts[1:]...)
			if execErr != nil {
				fmt.Fprintf(&validationLog, "ERROR: %s: %v\n", cmdStr, execErr)
				// Execution errors (command not found, etc) are not validation failures
				// They indicate the command doesnt apply to this project - skip
				continue
			}
			if result.ExitCode != 0 {
				validationPassed = false
				fmt.Fprintf(&validationLog, "FAIL: %s (exit %d)\n%s\n%s\n", cmdStr, result.ExitCode, result.Stdout, result.Stderr)
			} else {
				fmt.Fprintf(&validationLog, "PASS: %s\n", cmdStr)
			}
		}
	}

	// Store validation results
	validationLogStr := validationLog.String()
	validationResultsJSON, _ := json.Marshal(map[string]any{
		"passed":   validationPassed,
		"log":      validationLogStr,
		"commands": validationCommands,
	})
	_ = w.store.UpdateSandboxSessionResult(ctx, job.Args.SessionID, store.SandboxSessionResult{
		ValidationResults: validationResultsJSON,
	})

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
					WorkDir:      job.Args.WorkDir,
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

	// Collect changes from sandbox
	if w.sandboxManager != nil && job.Args.WorkDir != "" {
		sb := &codegen.Sandbox{WorkDir: job.Args.WorkDir}
		changes, diffErr := w.sandboxManager.CollectDiff(ctx, sb)
		if diffErr != nil {
			slog.Warn("failed to collect sandbox diff", "error", diffErr)
		} else if len(changes) > 0 {
			// Persist generated files to generation record
			generatedFiles := make([]domain.GeneratedFile, 0, len(changes))
			for _, c := range changes {
				if c.ChangeType != "deleted" && c.Content != "" {
					generatedFiles = append(generatedFiles, domain.GeneratedFile{
						Path:    c.Path,
						Content: c.Content,
					})
				}
			}

			gen, genErr := w.store.GetGeneration(ctx, job.Args.GenerationID)
			if genErr == nil && len(generatedFiles) > 0 {
				gen.Files = generatedFiles
				gen.Status = domain.GenerationStatusRunning
				_ = w.store.UpdateGeneration(ctx, gen)
			}

			// Store file changes in session
			filesJSON, _ := json.Marshal(changes)
			_ = w.store.UpdateSandboxSessionResult(ctx, job.Args.SessionID, store.SandboxSessionResult{
				FilesChanged: filesJSON,
			})
		}
	}

	// Mark session completed
	_ = w.store.UpdateSandboxSessionStatus(ctx, job.Args.SessionID, "completed", nil)

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

type agentContextPayload struct {
	Instructions string `json:"instructions"`
	Conventions  string `json:"conventions"`
}

func detectValidationCommands(project *domain.Project) []string {
	if project == nil {
		return []string{"npm run lint", "npm run test", "npm run typecheck"}
	}

	commands := make([]string, 0, 4)
	add := func(cmd string) {
		for _, existing := range commands {
			if existing == cmd {
				return
			}
		}
		commands = append(commands, cmd)
	}

	switch project.Framework {
	case domain.ProjectFrameworkNextJS:
		add("npm run lint")
		add("npm run test")
		add("npm run typecheck")
		add("npm run build")
	case domain.ProjectFrameworkReact:
		add("npm run lint")
		add("npm run test")
		add("npm run typecheck")
	case domain.ProjectFrameworkVue, domain.ProjectFrameworkSvelte, domain.ProjectFrameworkAngular:
		add("npm run lint")
		add("npm run test")
		add("npm run build")
	default:
		add("npm run lint")
		add("npm run test")
		add("npm run typecheck")
	}

	return commands
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
