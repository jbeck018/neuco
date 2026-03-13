package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/generation"
	"github.com/neuco-ai/neuco/internal/store"
)

// FetchSpecWorker loads the spec and project context for code generation.
type FetchSpecWorker struct {
	river.WorkerDefaults[FetchSpecJobArgs]
	store *store.Store
}

func NewFetchSpecWorker(s *store.Store) *FetchSpecWorker {
	return &FetchSpecWorker{store: s}
}

func (w *FetchSpecWorker) Work(ctx context.Context, job *river.Job[FetchSpecJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("fetching spec for codegen", "spec_id", job.Args.SpecID)

	spec, err := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	_ = spec // Used by downstream tasks via DB lookup

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: index_repo
	client := getRiverClient()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var indexTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "index_repo" {
					indexTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, IndexRepoJobArgs{
			SpecID:    job.Args.SpecID,
			ProjectID: job.Args.ProjectID,
			RunID:     job.Args.RunID,
			TaskID:    indexTaskID,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain index_repo job", "error", err)
		}
	}

	return nil
}

// IndexRepoWorker indexes the user's GitHub repository.
type IndexRepoWorker struct {
	river.WorkerDefaults[IndexRepoJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewIndexRepoWorker(s *store.Store, cfg *config.Config) *IndexRepoWorker {
	return &IndexRepoWorker{store: s, cfg: cfg}
}

func (w *IndexRepoWorker) Work(ctx context.Context, job *river.Job[IndexRepoJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("indexing repo", "project_id", job.Args.ProjectID)

	project, err := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	if project.GitHubRepo == "" {
		slog.Warn("no github repo configured, skipping index")
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	if w.cfg.GitHubAppID == "" || w.cfg.GitHubAppPrivateKeyPath == "" {
		slog.Info("github app not configured, skipping repo indexing",
			"project_id", job.Args.ProjectID,
			"repo", project.GitHubRepo,
		)
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	// Resolve the GitHub App installation for this project's org.
	installationID, err := w.store.GetProjectGitHubInstallation(ctx, job.Args.ProjectID)
	if err != nil {
		slog.Warn("no github app installation found, skipping repo indexing",
			"project_id", job.Args.ProjectID,
			"repo", project.GitHubRepo,
			"error", err,
		)
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	owner, repo, err := parseOwnerRepo(project.GitHubRepo)
	if err != nil {
		slog.Warn("invalid github_repo format, skipping indexing",
			"github_repo", project.GitHubRepo,
			"error", err,
		)
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	ghSvc, err := generation.NewGitHubService(w.cfg.GitHubAppID, w.cfg.GitHubAppPrivateKeyPath)
	if err != nil {
		slog.Error("failed to create github service",
			"error", err,
			"project_id", job.Args.ProjectID,
		)
		// Non-fatal: proceed without index.
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	indexer := generation.NewIndexer(ghSvc)
	repoIndex, err := indexer.IndexRepo(ctx, installationID, owner, repo, "")
	if err != nil {
		slog.Error("repo indexing failed",
			"error", err,
			"project_id", job.Args.ProjectID,
			"repo", project.GitHubRepo,
		)
		// Non-fatal: code generation can proceed without the index.
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainBuildContext(ctx, w.store, job.Args)
		return nil
	}

	// Persist the index JSON in the pipeline run's metadata so BuildContextWorker
	// can retrieve it without an additional DB lookup.
	indexJSON, err := json.Marshal(repoIndex)
	if err == nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		if run != nil {
			// Merge the repo_index key into existing metadata by unmarshalling
			// the current JSONB blob, adding the key, and re-marshalling.
			meta := map[string]json.RawMessage{}
			if len(run.Metadata) > 0 {
				_ = json.Unmarshal(run.Metadata, &meta)
			}
			meta["repo_index"] = indexJSON
			if merged, mergeErr := json.Marshal(meta); mergeErr == nil {
				// Best-effort update; failure is non-fatal.
				_ = w.store.UpdatePipelineRunMetadata(ctx, job.Args.RunID, json.RawMessage(merged))
			}
		}
	}

	slog.Info("repo indexing completed",
		"project_id", job.Args.ProjectID,
		"repo", project.GitHubRepo,
		"components", len(repoIndex.Components),
		"stories", len(repoIndex.Stories),
	)

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	chainBuildContextWithIndex(ctx, w.store, job.Args, repoIndex)
	return nil
}

// parseOwnerRepo is a package-level adapter delegating to the generation
// package helper to avoid a separate import cycle.
func parseOwnerRepo(ownerRepo string) (owner, repo string, err error) {
	// Inline split to avoid exposing the unexported generation.parseOwnerRepo.
	parts := splitOwnerRepo(ownerRepo)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("parseOwnerRepo: expected 'owner/repo', got %q", ownerRepo)
	}
	return parts[0], parts[1], nil
}

func splitOwnerRepo(s string) []string {
	result := make([]string, 0, 2)
	slash := -1
	for i, c := range s {
		if c == '/' {
			slash = i
			break
		}
	}
	if slash <= 0 || slash == len(s)-1 {
		return nil
	}
	result = append(result, s[:slash], s[slash+1:])
	return result
}

func chainBuildContext(ctx context.Context, s *store.Store, args IndexRepoJobArgs) {
	chainBuildContextWithIndex(ctx, s, args, nil)
}

func chainBuildContextWithIndex(ctx context.Context, s *store.Store, args IndexRepoJobArgs, index *generation.RepoIndex) {
	client := getRiverClient()
	if client == nil {
		return
	}

	run, _ := s.GetPipelineRun(ctx, args.RunID)
	var buildTaskID uuid.UUID
	if run != nil {
		for _, t := range run.Tasks {
			if t.Name == "build_context" {
				buildTaskID = t.ID
				break
			}
		}
	}

	jobArgs := BuildContextJobArgs{
		SpecID:    args.SpecID,
		ProjectID: args.ProjectID,
		RunID:     args.RunID,
		TaskID:    buildTaskID,
	}
	if index != nil {
		if b, err := json.Marshal(index); err == nil {
			jobArgs.RepoIndexJSON = string(b)
		}
	}

	_, err := client.Insert(ctx, jobArgs, &river.InsertOpts{Queue: "codegen"})
	if err != nil {
		slog.Error("failed to chain build_context job", "error", err)
	}
}

// BuildContextWorker selects relevant codebase context for generation.
type BuildContextWorker struct {
	river.WorkerDefaults[BuildContextJobArgs]
	store *store.Store
}

func NewBuildContextWorker(s *store.Store) *BuildContextWorker {
	return &BuildContextWorker{store: s}
}

func (w *BuildContextWorker) Work(ctx context.Context, job *river.Job[BuildContextJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("building context", "spec_id", job.Args.SpecID)

	spec, err := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// Attempt to build codebase context from the repo index delivered by
	// IndexRepoWorker. When no index is available (GitHub App not configured or
	// not installed), codegenContext is empty and the LLM generates from the
	// spec alone.
	var codegenContext string
	if job.Args.RepoIndexJSON != "" {
		var index generation.RepoIndex
		if err := json.Unmarshal([]byte(job.Args.RepoIndexJSON), &index); err == nil {
			codegenContext = generation.BuildCodegenContext(&index, spec)
		} else {
			slog.Warn("build_context: failed to unmarshal repo index", "error", err)
		}
	}

	if codegenContext != "" {
		slog.Info("build_context: codebase context attached",
			"spec_id", job.Args.SpecID,
			"context_bytes", len(codegenContext),
		)
	} else {
		slog.Info("build_context: no codebase context available, generating from spec only",
			"spec_id", job.Args.SpecID,
		)
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: generate_code, forwarding the context string so the worker can
	// embed it in the prompt without re-querying the index.
	client := getRiverClient()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var genTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "generate_code" {
					genTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, GenerateCodeJobArgs{
			SpecID:         job.Args.SpecID,
			ProjectID:      job.Args.ProjectID,
			RunID:          job.Args.RunID,
			TaskID:         genTaskID,
			CodegenContext: codegenContext,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain generate_code job", "error", err)
		}
	}

	return nil
}

// GenerateCodeWorker uses an LLM to generate component code from the spec.
type GenerateCodeWorker struct {
	river.WorkerDefaults[GenerateCodeJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewGenerateCodeWorker(s *store.Store, cfg *config.Config) *GenerateCodeWorker {
	return &GenerateCodeWorker{store: s, cfg: cfg}
}

func (w *GenerateCodeWorker) Work(ctx context.Context, job *river.Job[GenerateCodeJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("generating code", "spec_id", job.Args.SpecID)

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

	// Generate code via LLM, enriched with codebase context when available.
	llmStart := time.Now()
	files, llmResp, err := generateCodeViaLLM(ctx, w.cfg.AnthropicAPIKey, spec, project, job.Args.CodegenContext)
	llmLatency := trackDuration(llmStart)
	if llmResp != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		recordLLMCall(ctx, w.store, job.Args.ProjectID,
			ptrUUID(job.Args.RunID), ptrUUID(job.Args.TaskID),
			domain.LLMProviderAnthropic, "claude-sonnet-4-6-20250514",
			domain.LLMCallTypeCodegen,
			llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
			errMsg)
	}
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// Create generation record
	gen := &domain.Generation{
		ID:            uuid.New(),
		SpecID:        job.Args.SpecID,
		ProjectID:     job.Args.ProjectID,
		PipelineRunID: job.Args.RunID,
		Status:        domain.GenerationStatusRunning,
		Files:         files,
	}

	if err := w.store.CreateGeneration(ctx, gen); err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: create_pr
	client := getRiverClient()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var prTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "create_pr" {
					prTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, CreatePRJobArgs{
			SpecID:       job.Args.SpecID,
			ProjectID:    job.Args.ProjectID,
			GenerationID: gen.ID,
			RunID:        job.Args.RunID,
			TaskID:       prTaskID,
		}, &river.InsertOpts{Queue: "codegen"})
		if err != nil {
			slog.Error("failed to chain create_pr job", "error", err)
		}
	}

	return nil
}

func generateCodeViaLLM(ctx context.Context, apiKey string, spec *domain.Spec, project *domain.Project, codegenContext string) ([]domain.GeneratedFile, *anthropicResponse, error) {
	if apiKey == "" {
		return []domain.GeneratedFile{
			{
				Path:    fmt.Sprintf("src/components/%s.tsx", sanitizeFileName(spec.ProposedSolution)),
				Content: "// API key not configured. Generated code would appear here.",
			},
		}, nil, nil
	}

	userStoriesJSON, _ := json.Marshal(spec.UserStories)
	acJSON, _ := json.Marshal(spec.AcceptanceCriteria)

	contextSection := ""
	if codegenContext != "" {
		contextSection = "\n" + codegenContext + "\n"
	}

	prompt := fmt.Sprintf(`You are a senior frontend engineer. Generate production-ready UI code based on this spec.

## Project Configuration
- Framework: %s
- Styling: %s
%s
## Spec
- Problem: %s
- Solution: %s
- User Stories: %s
- Acceptance Criteria: %s
- UI Changes: %s

## Output Requirements
Generate the following files as a JSON array:
[
  {"path": "relative/path/to/Component.tsx", "content": "full file content"},
  {"path": "relative/path/to/Component.stories.tsx", "content": "full storybook file"},
  {"path": "relative/path/to/Component.test.tsx", "content": "full test file"},
  {"path": "relative/path/to/types.ts", "content": "TypeScript types if needed"}
]

Rules:
- Use the project's framework and styling approach
- Follow the coding patterns shown in the codebase context above when present
- Include all imports
- Storybook stories should cover: Default, Loading, Error, Empty states
- Tests should use Testing Library
- Code must be immediately usable — no placeholder comments
- Respond with ONLY the JSON array, no other text`, project.Framework, project.Styling, contextSection, spec.ProblemStatement, spec.ProposedSolution, string(userStoriesJSON), string(acJSON), spec.UIChanges)

	payload := map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": 8192,
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

	text := result.Content[0].Text
	// Extract JSON array
	if idx := findJSONArrayStart(text); idx >= 0 {
		text = text[idx:]
	}
	if idx := findJSONArrayEnd(text); idx >= 0 {
		text = text[:idx+1]
	}

	var files []domain.GeneratedFile
	if err := json.Unmarshal([]byte(text), &files); err != nil {
		return []domain.GeneratedFile{
			{Path: "generated-output.txt", Content: result.Content[0].Text},
		}, &result, nil
	}

	return files, &result, nil
}

func findJSONArrayStart(s string) int {
	for i, c := range s {
		if c == '[' {
			return i
		}
	}
	return -1
}

func findJSONArrayEnd(s string) int {
	depth := 0
	started := false
	for i, c := range s {
		switch c {
		case '[':
			depth++
			started = true
		case ']':
			depth--
			if started && depth == 0 {
				return i
			}
		}
	}
	return -1
}

func sanitizeFileName(s string) string {
	if len(s) > 30 {
		s = s[:30]
	}
	result := make([]byte, 0, len(s))
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			result = append(result, byte(c))
		}
	}
	if len(result) == 0 {
		return "Component"
	}
	return string(result)
}

// CreatePRWorker creates a GitHub PR with the generated files.
type CreatePRWorker struct {
	river.WorkerDefaults[CreatePRJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewCreatePRWorker(s *store.Store, cfg *config.Config) *CreatePRWorker {
	return &CreatePRWorker{store: s, cfg: cfg}
}

func (w *CreatePRWorker) Work(ctx context.Context, job *river.Job[CreatePRJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("creating PR",
		"generation_id", job.Args.GenerationID,
		"project_id", job.Args.ProjectID,
	)

	project, err := w.store.GetProjectInternal(ctx, job.Args.ProjectID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	gen, err := w.store.GetGeneration(ctx, job.Args.GenerationID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// Compute a stable branch name from the generation ID so retries are
	// idempotent (CreateBranch is a no-op when the branch already exists).
	branchName := fmt.Sprintf("neuco/gen-%s", gen.ID.String()[:8])

	if project.GitHubRepo == "" || w.cfg.GitHubAppID == "" || w.cfg.GitHubAppPrivateKeyPath == "" {
		slog.Warn("github not configured, completing generation without PR",
			"github_repo", project.GitHubRepo,
			"app_id_set", w.cfg.GitHubAppID != "",
		)
		gen.Status = domain.GenerationStatusCompleted
		gen.BranchName = branchName
		now := time.Now()
		gen.CompletedAt = &now
		if err := w.store.UpdateGeneration(ctx, gen); err != nil {
			slog.Error("failed to update generation", "error", err)
		}
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainNotify(ctx, w.store, job.Args)
		return nil
	}

	installationID, err := w.store.GetProjectGitHubInstallation(ctx, job.Args.ProjectID)
	if err != nil {
		slog.Warn("no github installation found, completing without PR",
			"project_id", job.Args.ProjectID,
			"error", err,
		)
		gen.Status = domain.GenerationStatusCompleted
		gen.BranchName = branchName
		now := time.Now()
		gen.CompletedAt = &now
		if updateErr := w.store.UpdateGeneration(ctx, gen); updateErr != nil {
			slog.Error("failed to update generation", "error", updateErr)
		}
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		chainNotify(ctx, w.store, job.Args)
		return nil
	}

	owner, repo, err := parseOwnerRepo(project.GitHubRepo)
	if err != nil {
		err = fmt.Errorf("invalid github_repo %q: %w", project.GitHubRepo, err)
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	ghSvc, err := generation.NewGitHubService(w.cfg.GitHubAppID, w.cfg.GitHubAppPrivateKeyPath)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: init github service: %w", err)
	}

	ghClient, err := ghSvc.GetInstallationClient(ctx, installationID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: get installation client: %w", err)
	}

	// Determine the default branch to use as the PR base.
	repoInfo, _, err := ghClient.Repositories.Get(ctx, owner, repo)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: get repo info: %w", err)
	}
	baseBranch := repoInfo.GetDefaultBranch()
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Step 1: Create the branch (idempotent).
	if err := ghSvc.CreateBranch(ctx, ghClient, owner, repo, baseBranch, branchName); err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: create branch: %w", err)
	}

	// Step 2: Commit generated files.
	fileMap := make(map[string]string, len(gen.Files))
	for _, f := range gen.Files {
		fileMap[f.Path] = f.Content
	}

	spec, specErr := w.store.GetSpecInternal(ctx, job.Args.SpecID)
	commitMsg := "feat: neuco generated code"
	if specErr == nil && spec.ProposedSolution != "" {
		summary := spec.ProposedSolution
		if len(summary) > 72 {
			summary = summary[:72]
		}
		commitMsg = fmt.Sprintf("feat: %s", summary)
	}

	if err := ghSvc.CommitFiles(ctx, ghClient, owner, repo, branchName, commitMsg, fileMap); err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: commit files: %w", err)
	}

	// Step 3: Open draft PR.
	prTitle := commitMsg
	prBody := buildPRBody(gen, spec)
	pr, err := ghSvc.CreatePullRequest(ctx, ghClient, owner, repo, prTitle, prBody, branchName, baseBranch)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return fmt.Errorf("CreatePRWorker: create pr: %w", err)
	}

	slog.Info("PR created",
		"pr_number", pr.GetNumber(),
		"pr_url", pr.GetHTMLURL(),
		"branch", branchName,
	)

	prNumber := pr.GetNumber()
	gen.Status = domain.GenerationStatusCompleted
	gen.BranchName = branchName
	gen.PRNumber = &prNumber
	gen.PRURL = pr.GetHTMLURL()
	now := time.Now()
	gen.CompletedAt = &now
	if err := w.store.UpdateGeneration(ctx, gen); err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	chainNotify(ctx, w.store, job.Args)
	return nil
}

// buildPRBody constructs a Markdown pull request description from the
// generation record and its associated spec.
func buildPRBody(gen *domain.Generation, spec *domain.Spec) string {
	var sb strings.Builder
	sb.WriteString("## Neuco Generated Code\n\n")
	sb.WriteString("> This PR was opened automatically by [Neuco](https://neuco.ai).\n\n")

	if spec != nil {
		if spec.ProblemStatement != "" {
			sb.WriteString("### Problem\n\n")
			sb.WriteString(spec.ProblemStatement)
			sb.WriteString("\n\n")
		}
		if spec.ProposedSolution != "" {
			sb.WriteString("### Solution\n\n")
			sb.WriteString(spec.ProposedSolution)
			sb.WriteString("\n\n")
		}
		if spec.UIChanges != "" {
			sb.WriteString("### UI Changes\n\n")
			sb.WriteString(spec.UIChanges)
			sb.WriteString("\n\n")
		}
	}

	if len(gen.Files) > 0 {
		sb.WriteString("### Generated Files\n\n")
		for _, f := range gen.Files {
			fmt.Fprintf(&sb, "- `%s`\n", f.Path)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "**Generation ID:** `%s`\n", gen.ID.String())
	return sb.String()
}

func chainNotify(ctx context.Context, s *store.Store, args CreatePRJobArgs) {
	client := getRiverClient()
	if client == nil {
		return
	}

	run, _ := s.GetPipelineRun(ctx, args.RunID)
	var notifyTaskID uuid.UUID
	if run != nil {
		for _, t := range run.Tasks {
			if t.Name == "notify" {
				notifyTaskID = t.ID
				break
			}
		}
	}

	_, err := client.Insert(ctx, NotifyJobArgs{
		ProjectID:    args.ProjectID,
		GenerationID: args.GenerationID,
		RunID:        args.RunID,
		TaskID:       notifyTaskID,
	}, &river.InsertOpts{Queue: "default"})
	if err != nil {
		slog.Error("failed to chain notify job", "error", err)
	}
}

// NotifyWorker sends notifications on generation completion.
type NotifyWorker struct {
	river.WorkerDefaults[NotifyJobArgs]
	store *store.Store
	cfg   *config.Config
}

func NewNotifyWorker(s *store.Store, cfg *config.Config) *NotifyWorker {
	return &NotifyWorker{store: s, cfg: cfg}
}

func (w *NotifyWorker) Work(ctx context.Context, job *river.Job[NotifyJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("sending notification", "generation_id", job.Args.GenerationID)

	gen, err := w.store.GetGeneration(ctx, job.Args.GenerationID)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return err
	}

	// Enqueue PR-created email notification via the async email job.
	if gen.PRURL != "" {
		if err := w.enqueuePRNotification(ctx, gen); err != nil {
			slog.Error("failed to enqueue PR notification email",
				"generation_id", gen.ID,
				"error", err,
			)
			// Non-fatal — notification failure does not fail the pipeline task.
		}
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)
	CheckPipelineCompletion(ctx, w.store, job.Args.RunID)

	// Trigger copilot review on the generation
	client := getRiverClient()
	if client != nil {
		_, err := client.Insert(ctx, CopilotReviewJobArgs{
			ProjectID:  job.Args.ProjectID,
			TargetType: "generation",
			TargetID:   job.Args.GenerationID,
		}, &river.InsertOpts{Queue: "default"})
		if err != nil {
			slog.Error("failed to enqueue copilot review", "error", err)
		}
	}

	return nil
}

// enqueuePRNotification enqueues an async email job for the PR-created notification.
func (w *NotifyWorker) enqueuePRNotification(ctx context.Context, gen *domain.Generation) error {
	project, err := w.store.GetProjectInternal(ctx, gen.ProjectID)
	if err != nil {
		return fmt.Errorf("enqueuePRNotification: get project: %w", err)
	}

	user, err := w.store.GetUserByID(ctx, project.CreatedBy)
	if err != nil {
		return fmt.Errorf("enqueuePRNotification: get user: %w", err)
	}
	if user.Email == "" {
		return nil
	}

	prNumber := 0
	if gen.PRNumber != nil {
		prNumber = *gen.PRNumber
	}

	return EnqueueEmail(ctx, "pr_created", map[string]interface{}{
		"email":        user.Email,
		"project_name": project.Name,
		"pr_url":       gen.PRURL,
		"pr_number":    prNumber,
		"files_count":  len(gen.Files),
	})
}
