# Neuco Codegen v2: Agent Sandbox Platform

> Implementation plan for transforming Neuco's code generation from a single-shot LLM prompt
> into a sandboxed agent-orchestration platform where users connect their preferred CLI coding agent.

## Table of Contents
1. Vision & Goals
2. Architecture Overview
3. Agent Provider System
4. Sandbox System
5. Rich Context Builder
6. Database Migrations
7. Worker Pipeline
8. API Endpoints
9. Frontend Changes
10. Security
11. Configuration
12. Package Structure
13. Implementation Phases
14. Migration Strategy
15. Success Metrics

---

## 1. Vision & Goals

### Current State
Neuco's code generation uses a single-shot LLM prompt architecture:
- One API call to Claude Sonnet with ~4,000 tokens of metadata-only context
- No actual source code in the prompt (only props lists, import names, file sizes)
- No code execution, testing, or validation before PR creation
- No iteration or self-correction loop
- Hardcoded model selection

### Target State
Neuco orchestrates the user's preferred CLI coding agent inside a sandboxed environment:
- Users connect Claude Code, OpenAI Codex, Gemini CLI, OpenCode, Slate CLI, Aider, or any custom CLI
- Users bring their own API keys (encrypted at rest)
- Agent runs in a real environment with the repo cloned and dependencies installed
- Full feedback loop: generate code -> run tests -> see failures -> fix -> iterate
- Real-time streaming of agent progress to the frontend via WebSocket
- Agents see 50K+ tokens of actual codebase context (real file contents, not metadata)
- Validation gates before PR creation (tests, lint, typecheck must pass)

### What We Keep
- Signal -> Candidate -> Spec pipeline (untouched, it's excellent)
- GitHub PR creation via Git Data API (reused as final pipeline step)
- Pipeline tracking (pipeline_runs + pipeline_tasks)
- Copilot review system
- Notification system

---

## 2. Architecture Overview

```text
+-----------------------------------------------------------------------------------+
|                                    Neuco API                                      |
|-----------------------------------------------------------------------------------|
| REST (Chi)                                                                        |
| - POST /projects/:id/generate-v2                                                  |
| - GET  /generations/:id/stream (WebSocket)                                        |
| - GET/PUT /projects/:id/agent-config                                               |
+-----------------------------------+-----------------------------------------------+
                                    |
                                    | enqueue River jobs
                                    v
+-----------------------------------------------------------------------------------+
|                             Neuco Worker (River 0.31)                             |
|-----------------------------------------------------------------------------------|
| Jobs: FetchSpec -> IndexRepo -> BuildRichContext -> ProvisionSandbox -> RunAgent  |
|       -> Validate -> CreatePR -> Notify                                            |
+---------------------+---------------------------+------------------+--------------+
                      |                           |                  |
                      | resolves provider         | provisions       | streams logs/events
                      v                           v                  v
            +---------------------+      +----------------+   +--------------------+
            |  Provider Registry  |      | Sandbox Manager|   |  Session Manager   |
            |---------------------|      |----------------|   |--------------------|
            | Claude Code         |      | E2B (primary)  |   | WS channels by     |
            | Codex               |      | Docker fallback|   | generation_id      |
            | Gemini CLI          |      | Local worktree |   | progress fanout    |
            | OpenCode            |      +-------+--------+   +---------+----------+
            | Slate CLI           |              |                      |
            | Aider               |              | launch sandbox       |
            | Generic CLI         |              v                      |
            +---------------------+      +-------------------------+    |
                                         |    Sandbox Runtime      |<---+
                                         |-------------------------|
                                         | Repo checkout           |
                                         | INSTRUCTIONS.md         |
                                         | CONVENTIONS.md          |
                                         | CLI Agent process       |
                                         | test/lint/typecheck     |
                                         +-----------+-------------+
                                                     |
                                                     | validated diff + commit
                                                     v
                                           +------------------------+
                                           |       GitHub API       |
                                           |------------------------|
                                           | branch + tree + commit |
                                           | PR creation            |
                                           +------------------------+
```

### End-to-End Data Flow
1. User clicks **Generate** in frontend for a project/spec.
2. API creates a `generation` row (`status=queued`) and a `pipeline_run`.
3. API enqueues River job chain for generation v2.
4. `FetchSpec` loads full `Spec` (problem statement, solution, stories, acceptance criteria, UI changes).
5. `IndexRepo` builds/loads `RepoIndex` signals (components, stories, type files, design tokens, framework, styling).
6. `BuildRichContext` fetches actual file contents and composes 50K+ token context pack.
7. Worker resolves effective `AgentConfig` (project override, then org default).
8. Provider registry loads selected provider and validates binary/env requirements.
9. Sandbox manager provisions environment (E2B or Docker/local fallback).
10. Worker writes repository snapshot, `INSTRUCTIONS.md`, `CONVENTIONS.md`, and task files into sandbox.
11. Worker executes provider-specific headless command in sandbox.
12. Provider parser streams structured progress events to Session Manager.
13. Session Manager publishes WebSocket events to UI in real time.
14. After agent pass, worker runs validation commands (tests/lint/typecheck).
15. If validation fails and retry budget remains, worker re-prompts agent with failures and iterates.
16. On success, worker collects git diff and changed files from sandbox.
17. Worker creates branch/commit/tree via GitHub Git Data API.
18. Worker opens PR, updates `generation.pr_url`, stores generated files metadata.
19. Notify step sends final status + PR link and closes stream session.
20. Generation marked `completed` or `failed` with full execution logs retained.

---

## 3. Agent Provider System

### 3.1 Core Interface

```go
// internal/codegen/provider.go
package codegen

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
)

// AgentProvider defines the contract for a CLI coding agent integration.
type AgentProvider interface {
	// Name returns the provider identifier (e.g. "claude-code", "codex", "gemini").
	Name() string

	// DisplayName returns a human-friendly name (e.g. "Claude Code", "OpenAI Codex").
	DisplayName() string

	// ValidateConfig checks that the agent configuration is valid (API key format, etc).
	ValidateConfig(ctx context.Context, cfg AgentConfig) error

	// InstallInstructions returns human-readable install steps.
	InstallInstructions() string

	// DetectInstalled checks if the agent binary is available in the given PATH.
	DetectInstalled(pathEnv string) bool

	// BuildCommand constructs the exec.Cmd to launch the agent headlessly.
	BuildCommand(req ExecutionRequest) (*exec.Cmd, error)

	// ParseOutput reads the agent's stdout and emits structured progress events.
	ParseOutput(r io.Reader) <-chan ProgressEvent
}

type AgentConfig struct {
	ID              uuid.UUID         `json:"id"`
	OrgID           uuid.UUID         `json:"org_id"`
	ProjectID       *uuid.UUID        `json:"project_id,omitempty"`
	Provider        string            `json:"provider"`
	DisplayName     string            `json:"display_name"`
	Enabled         bool              `json:"enabled"`
	BinaryPath      string            `json:"binary_path"`
	PathEnv         string            `json:"path_env,omitempty"`
	WorkingDir      string            `json:"working_dir,omitempty"`
	Model           string            `json:"model,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	SecretsRef      map[string]string `json:"secrets_ref,omitempty"`
	InstallHint     string            `json:"install_hint,omitempty"`
	IsDefault       bool              `json:"is_default"`
	LastValidatedAt *time.Time        `json:"last_validated_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type ExecutionRequest struct {
	GenerationID      uuid.UUID         `json:"generation_id"`
	OrgID             uuid.UUID         `json:"org_id"`
	ProjectID         uuid.UUID         `json:"project_id"`
	Provider          string            `json:"provider"`
	Model             string            `json:"model,omitempty"`
	SandboxID         string            `json:"sandbox_id"`
	SandboxPath       string            `json:"sandbox_path"`
	PromptFile        string            `json:"prompt_file"`
	ContextFiles      []string          `json:"context_files,omitempty"`
	MaxIterations     int               `json:"max_iterations"`
	TimeoutSeconds    int               `json:"timeout_seconds"`
	AdditionalArgs    []string          `json:"additional_args,omitempty"`
	Environment       map[string]string `json:"environment,omitempty"`
	RepoDefaultBranch string            `json:"repo_default_branch,omitempty"`
	Spec              domain.Spec       `json:"spec"`
}

type ExecutionResult struct {
	GenerationID      uuid.UUID       `json:"generation_id"`
	Provider          string          `json:"provider"`
	SandboxID         string          `json:"sandbox_id"`
	Success           bool            `json:"success"`
	ExitCode          int             `json:"exit_code"`
	Duration          time.Duration   `json:"duration"`
	Iterations        int             `json:"iterations"`
	Summary           string          `json:"summary,omitempty"`
	StdoutTail        string          `json:"stdout_tail,omitempty"`
	StderrTail        string          `json:"stderr_tail,omitempty"`
	ValidationPassed  bool            `json:"validation_passed"`
	ValidationResults []ProgressEvent `json:"validation_results,omitempty"`
	FileChanges       []FileChange    `json:"file_changes,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	CompletedAt       time.Time       `json:"completed_at"`
}

type FileChange struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"` // added, modified, deleted, renamed
	OldPath    string `json:"old_path,omitempty"`
	Diff       string `json:"diff,omitempty"`
	Content    string `json:"content,omitempty"`
	Binary     bool   `json:"binary"`
	Size       int64  `json:"size"`
}

type ProgressEvent struct {
	GenerationID uuid.UUID  `json:"generation_id"`
	SandboxID    string     `json:"sandbox_id"`
	Provider     string     `json:"provider"`
	Phase        string     `json:"phase"`   // setup, planning, coding, validating, committing
	Level        string     `json:"level"`   // debug, info, warn, error
	Message      string     `json:"message"`
	Raw          string     `json:"raw,omitempty"`
	Percent      int        `json:"percent,omitempty"`
	FilePath     string     `json:"file_path,omitempty"`
	Step         string     `json:"step,omitempty"`
	Iteration    int        `json:"iteration,omitempty"`
	Timestamp    time.Time  `json:"timestamp"`
	Error        *EventError `json:"error,omitempty"`
}

type EventError struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Retry   bool   `json:"retry"`
}

type ProviderRegistry struct {
	providers map[string]AgentProvider
}

func NewProviderRegistry(list ...AgentProvider) *ProviderRegistry {
	m := make(map[string]AgentProvider, len(list))
	for _, p := range list {
		m[p.Name()] = p
	}
	return &ProviderRegistry{providers: m}
}

func (r *ProviderRegistry) Get(name string) (AgentProvider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

func (r *ProviderRegistry) List() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	return out
}
```

### 3.2 Built-in Providers

#### Claude Code
- **Binary name:** `claude`
- **Install command:** `npm install -g @anthropic-ai/claude-code`
- **Headless command:**
  ```bash
  claude -p --print --output-format stream-json --max-turns 12 --allowedTools "Read,Write,Edit,Bash" --model "$NEUCO_AGENT_MODEL" --append-system-prompt-file INSTRUCTIONS.md
  ```
- **Required env vars:** `ANTHROPIC_API_KEY` (required), `ANTHROPIC_BASE_URL` (optional), `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`
- **Output format:** streaming JSON events (`assistant`, `tool_use`, `result`, `error`) parsed incrementally
- **Detection method:** `exec.LookPath("claude")`, then `claude --version`
- **Notes:** best support for structured progress streaming; explicitly constrain tools + max turns to control runtime.

#### OpenAI Codex
- **Binary name:** `codex`
- **Install command:** `npm install -g @openai/codex`
- **Headless command:**
  ```bash
  codex exec --json --no-interactive --model "$NEUCO_AGENT_MODEL" --task-file INSTRUCTIONS.md --workdir .
  ```
- **Required env vars:** `OPENAI_API_KEY` (required), `OPENAI_BASE_URL` (optional)
- **Output format:** newline-delimited JSON with status and patch summaries
- **Detection method:** `exec.LookPath("codex")`, then `codex --help`
- **Notes:** normalize event schema because codex message structure differs from Claude stream format.

#### Gemini CLI
- **Binary name:** `gemini`
- **Install command:** `npm install -g @google/gemini-cli`
- **Headless command:**
  ```bash
  gemini -p --prompt-file INSTRUCTIONS.md --yolo --format json --model "$NEUCO_AGENT_MODEL"
  ```
- **Required env vars:** `GEMINI_API_KEY` (required) or `GOOGLE_API_KEY`; optionally `GOOGLE_GENAI_USE_VERTEXAI`
- **Output format:** JSON blocks with markdown/code payloads
- **Detection method:** `exec.LookPath("gemini")`, then `gemini --version`
- **Notes:** wrap output parser to handle mixed plain text + JSON modes depending on CLI version.

#### OpenCode
- **Binary name:** `opencode`
- **Install command:** `npm install -g opencode-ai`
- **Headless command:**
  ```bash
  opencode run --non-interactive --json --task INSTRUCTIONS.md --model "$NEUCO_AGENT_MODEL"
  ```
- **Required env vars:** provider-specific key(s), typically `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`
- **Output format:** JSON event stream, includes tool invocation and file write events
- **Detection method:** `exec.LookPath("opencode")`, then `opencode --version`
- **Notes:** use explicit model/provider mapping in config because OpenCode can broker multiple backends.

#### Slate CLI (Random Labs)
- **Binary name:** `slate`
- **Install command:** internal distribution / package manager command as provided by Random Labs
- **Headless command:**
  ```bash
  slate run --task-file INSTRUCTIONS.md --json --max-steps 40 --sandbox-mode external
  ```
- **Required env vars:** `SLATE_API_KEY` (required), optional endpoint override
- **Output format:** structured JSON events (`thinking`, `action`, `observation`, `result`)
- **Detection method:** `exec.LookPath("slate")`, then `slate version`
- **Notes:** map Slate action/observation events into Neuco `ProgressEvent` phases.

#### Aider
- **Binary name:** `aider`
- **Install command:** `python -m pip install aider-install && aider-install`
- **Headless command:**
  ```bash
  aider --yes --message-file INSTRUCTIONS.md --model "$NEUCO_AGENT_MODEL" --no-pretty --analytics-disable
  ```
- **Required env vars:** model backend key (commonly `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`)
- **Output format:** text-first output; parser uses regex markers + git diff scan
- **Detection method:** `exec.LookPath("aider")`, then `aider --version`
- **Notes:** less structured stdout; rely on post-run diff and command exit status for truth.

#### Generic / Custom
- **Binary name:** user-defined (e.g. `my-agent`)
- **Install command:** user-provided in config UI
- **Headless command:**
  ```bash
  {{.BinaryPath}} {{join .Args " "}}
  ```
- **Required env vars:** user-defined secret map (encrypted at rest)
- **Output format:** `json`, `ndjson`, or `text` (parser selected via config)
- **Detection method:** `exec.LookPath(customBinary)` and optional user-defined health check arg
- **Notes:** escape/validate all custom args; deny shell metacharacters unless explicitly permitted by admin policy.

### 3.3 Provider Configuration Storage

```go
// internal/store/agent_config.go
package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/codegen"
)

func (s *Store) GetAgentConfig(ctx context.Context, orgID, projectID uuid.UUID) (*codegen.AgentConfig, error) {
	// SELECT ... FROM agent_configs WHERE org_id=$1 AND project_id=$2 AND enabled=true
	panic("implement")
}

func (s *Store) SetAgentConfig(ctx context.Context, cfg codegen.AgentConfig) error {
	// UPSERT by (org_id, project_id, provider)
	panic("implement")
}

func (s *Store) DeleteAgentConfig(ctx context.Context, orgID, projectID uuid.UUID, provider string) error {
	// DELETE FROM agent_configs WHERE org_id=$1 AND project_id=$2 AND provider=$3
	panic("implement")
}

func (s *Store) GetEffectiveConfig(ctx context.Context, orgID, projectID uuid.UUID) (*codegen.AgentConfig, error) {
	// 1) project-level config
	// 2) org-level default (project_id IS NULL)
	// 3) return not found
	panic("implement")
}
```

---

## 4. Sandbox System

### 4.1 Manager Interface

```go
// internal/codegen/sandbox/manager.go
package sandbox

import (
	"context"
	"time"
)

type SandboxManager interface {
	Provision(ctx context.Context, cfg SandboxConfig) (*Sandbox, error)
	WriteFiles(ctx context.Context, sb *Sandbox, files map[string]string) error
	Execute(ctx context.Context, sb *Sandbox, cmd string, args ...string) (*ExecResult, error)
	StreamLogs(ctx context.Context, sandboxID string) (<-chan LogEntry, error)
	CollectDiff(ctx context.Context, sb *Sandbox) ([]FileChange, error)
	Destroy(ctx context.Context, sandboxID string) error
}

type SandboxConfig struct {
	GenerationID    string            `json:"generation_id"`
	Provider        string            `json:"provider"`
	BaseImage       string            `json:"base_image"`
	CPU             int               `json:"cpu"`
	MemoryMB        int               `json:"memory_mb"`
	DiskMB          int               `json:"disk_mb"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	NetworkEnabled  bool              `json:"network_enabled"`
	Env             map[string]string `json:"env,omitempty"`
	RepoURL         string            `json:"repo_url"`
	RepoRef         string            `json:"repo_ref"`
	WorkingDir      string            `json:"working_dir"`
	InstallCommands []string          `json:"install_commands,omitempty"`
}

type Sandbox struct {
	ID          string            `json:"id"`
	Provider    string            `json:"provider"` // e2b, docker, local
	Workspace   string            `json:"workspace"`
	Status      string            `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type ExecResult struct {
	Command     string        `json:"command"`
	Args        []string      `json:"args"`
	ExitCode    int           `json:"exit_code"`
	Stdout      string        `json:"stdout"`
	Stderr      string        `json:"stderr"`
	Duration    time.Duration `json:"duration"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
}

type LogEntry struct {
	SandboxID string    `json:"sandbox_id"`
	Source    string    `json:"source"` // stdout, stderr, system
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type FileChange struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
	Diff       string `json:"diff,omitempty"`
	Content    string `json:"content,omitempty"`
}
```

### 4.2 E2B Integration (Primary)
- Use E2B SDK as primary runtime for secure isolated execution.
- Build custom Neuco template image:
  - `git`, `gh`, `node`, `pnpm`, `go`, `python`, `ripgrep`, `jq`
  - preinstalled supported agent CLIs where licensing permits
  - non-root user, restricted capabilities, workspace under `/workspace`
- Provision flow:
  1. `e2b.NewSandbox(template="neuco-codegen", timeout=...)`
  2. upload repo snapshot + context files
  3. execute install/bootstrap commands
  4. execute provider command
  5. stream logs/events over SDK websocket
  6. collect diff and artifacts
  7. terminate sandbox
- Lifecycle metadata stored on generation record: `sandbox_id`, `sandbox_provider=e2b`, `expires_at`, `template_version`.

### 4.3 Docker Fallback (Self-hosted)
- For on-prem or no E2B environments, use Docker runner on worker host.
- Create container per generation with resource limits:
  - `--cpus=2 --memory=4g --pids-limit=512 --network=bridge`
- Mount ephemeral workspace volume; copy repository snapshot into `/workspace`.
- Execute via `docker exec` under unprivileged user.
- Collect logs with `docker logs -f` and diff via in-container git commands.
- Hard timeout watchdog forcibly stops/removes container on overrun.

### 4.4 Local Development Mode
- Use git worktree for deterministic local dev execution.
- Create ephemeral path:
  - `.neuco/sandboxes/<generation-id>/worktree`
- Commands:
  - `git worktree add --detach <path> <base-ref>`
  - run agent + validation locally
  - `git -C <path> diff --binary`
  - `git worktree remove --force <path>`
- Never enabled in production; guarded by explicit config flag.

### 4.5 Sandbox Lifecycle (Steps 1-14)
1. **Resolve config**: load provider + sandbox backend + limits from effective project/org config.
2. **Create sandbox record**: persist `sandbox_id`, `status=provisioning`.
3. **Provision runtime**: call E2B/Docker/local backend with selected base image/template.
4. **Clone repository**:
   ```bash
   git clone --depth=1 --branch "$BASE_REF" "$REPO_URL" /workspace/repo
   ```
5. **Create generation branch**:
   ```bash
   git -C /workspace/repo checkout -b neuco/generation-$GEN_ID
   ```
6. **Install dependencies** (language-aware):
   ```bash
   pnpm install --frozen-lockfile || npm ci
   go mod download
   pip install -r requirements.txt || true
   ```
7. **Write context files**:
   - `/workspace/repo/INSTRUCTIONS.md`
   - `/workspace/repo/CONVENTIONS.md`
   - `/workspace/repo/.neuco/context/*.md`
8. **Pre-flight checks**:
   ```bash
   git -C /workspace/repo status --porcelain
   test -f /workspace/repo/INSTRUCTIONS.md
   ```
9. **Run agent command** (provider-specific headless invocation).
10. **Stream logs/events** continuously to websocket subscribers.
11. **Run validation gates**:
    ```bash
    pnpm test || npm test
    pnpm lint || npm run lint
    pnpm typecheck || npm run typecheck
    go test ./...
    ```
12. **Retry loop (if needed)**: append failure output to follow-up prompt and rerun agent until max iterations.
13. **Collect outputs**:
    ```bash
    git -C /workspace/repo diff --binary
    git -C /workspace/repo status --porcelain
    git -C /workspace/repo log --oneline -n 5
    ```
14. **Destroy sandbox**: upload artifacts, set terminal status, then terminate runtime and clean temp storage.

---

## 5. Rich Context Builder

### 5.1 From Metadata to Full Content
Current v1 context builder sends only lightweight metadata (~4K tokens). v2 must send real code content (~50K tokens target, configurable higher) so agents can reason about implementation details, existing patterns, and integration points.

**v2 strategy:**
- Start with spec-derived intent (features, affected domains, UI hints).
- Expand from repo index anchors (components, stories, types, design tokens).
- Add dependency graph neighbors (imports + callers/callees where available).
- Include policy files (README, CONTRIBUTING, lint/type configs).
- Budget by weighted relevance score and token estimate.
- Always include high-signal small files (types/interfaces/tests around target area).

```text
PSEUDOCODE: BuildRichContext(spec, repoIndex, repoFS, tokenBudget=50000)

1. candidates = []
2. keywords = ExtractKeywords(spec.problem_statement, spec.proposed_solution,
                              spec.user_stories, spec.acceptance_criteria, spec.ui_changes)
3. anchors = repoIndex.components + repoIndex.stories + repoIndex.type_files + repoIndex.design_tokens
4. for file in repoFS:
     score = 0
     if file.path in anchors: score += 50
     if PathMatchesKeywords(file.path, keywords): score += 25
     if ContentMatchesKeywords(file.content, keywords): score += 20
     if IsTestNearLikelyTarget(file.path): score += 15
     if IsConfigOrConventionFile(file.path): score += 10
     if file.size > MAX_FILE_SIZE: score -= 30
     if IsGeneratedOrVendor(file.path): score = -INF
     estTokens = EstimateTokens(file.content)
     candidates.append({file, score, estTokens})

5. sort candidates by score desc, estTokens asc
6. selected = []
7. used = 0
8. for c in candidates:
     if used + c.estTokens > tokenBudget: continue
     selected.append(c.file)
     used += c.estTokens

9. selected = EnsureCoverage(selected,
      mustInclude=["README", "package.json", "tsconfig", "go.mod", "router", "db models"]) 
10. chunks = ChunkLargeFiles(selected, maxChunkTokens=1200, withLineRanges=true)
11. return ContextBundle{
      manifest: BuildManifest(chunks),
      files: chunks,
      totalTokens: SumTokens(chunks),
      truncated: tokenBudgetReached
    }
```

### 5.2 INSTRUCTIONS.md Template

```md
# Neuco Code Generation Task

You are operating inside a real repository checkout. Implement the requested change safely and completely.

## Execution Contract
- Work only in this repository.
- Make minimal, high-confidence changes.
- Preserve existing architecture and conventions.
- Run validation commands before finishing.
- If validation fails, fix and re-run until passing or iteration budget is exhausted.

## Generation Metadata
- Generation ID: `{{.Generation.ID}}`
- Project ID: `{{.Generation.ProjectID}}`
- Spec ID: `{{.Generation.SpecID}}`
- Provider: `{{.Agent.Provider}}`
- Model: `{{.Agent.Model}}`
- Max Iterations: `{{.Execution.MaxIterations}}`

## Product Spec
### Problem Statement
{{.Spec.ProblemStatement}}

### Proposed Solution
{{.Spec.ProposedSolution}}

### User Stories
{{range .Spec.UserStories}}- {{.}}
{{end}}

### Acceptance Criteria
{{range .Spec.AcceptanceCriteria}}- {{.}}
{{end}}

### UI Changes
{{.Spec.UIChanges}}

## Repository Context
- Framework: `{{.RepoIndex.Framework}}`
- Styling: `{{.RepoIndex.Styling}}`
- Key Components:
{{range .RepoIndex.Components}}  - {{.}}
{{end}}
- Story Files:
{{range .RepoIndex.Stories}}  - {{.}}
{{end}}
- Type Files:
{{range .RepoIndex.TypeFiles}}  - {{.}}
{{end}}
- Design Tokens:
{{range .RepoIndex.DesignTokens}}  - {{.}}
{{end}}

## Files Included in Context
{{range .Context.Manifest}}- `{{.Path}}` ({{.StartLine}}-{{.EndLine}})
{{end}}

## Required Workflow
1. Read `CONVENTIONS.md` and all provided context files.
2. Plan the minimal set of file changes.
3. Implement changes.
4. Run validation commands:
   - `{{range .Validation.Commands}}`
   - `{{.}}`
   `{{end}}`
5. If failures occur, fix and rerun.
6. Summarize final changes and validation results.

## Output Requirements
- Ensure code compiles and tests pass.
- Do not leave TODO placeholders.
- Do not modify unrelated files.
- Provide a concise summary with:
  - files changed
  - behavior implemented
  - validation command results

## Constraints
- Follow existing project patterns for routing, data access, and typing.
- Respect linting and formatting configuration.
- Prefer incremental safe changes over broad refactors.
```

### 5.3 CONVENTIONS.md Auto-detection
`CONVENTIONS.md` is generated automatically from `RepoIndex` + light static analysis so each run gets explicit project norms.

Detection pipeline:
1. **Framework detection** from lockfiles and config (`svelte.config.*`, `next.config.*`, etc.).
2. **Styling detection** from dependency graph and token files (Tailwind, CSS modules, styled-components).
3. **Type system patterns** from TypeScript config strictness, `*.d.ts`, zod/io-ts usage.
4. **Backend conventions** from router setup (Chi), store patterns (`pgxpool` wrappers), migration style, and job orchestration style (River).
5. **Testing conventions** from test file naming, runners, and CI scripts.
6. **Lint/format conventions** from eslint/prettier/golangci configs.
7. **API conventions** from existing handler signatures, error response schema, and DTO naming.
8. **Code organization** from directory structure and import boundaries.

`CONVENTIONS.md` sections should include:
- Naming conventions (files, functions, components)
- API + handler patterns
- Store/query patterns
- Migration conventions
- Frontend component/state conventions
- Testing requirements and command matrix
- “Do/Don’t” examples extracted from real code snippets

This gives every agent run an explicit local style guide grounded in actual repository behavior, reducing off-pattern output and rework.

---

## 6. Database Migrations

### 6.1 Migration 000017_agent_codegen.up.sql

```sql
-- 000017_agent_codegen.up.sql

CREATE TABLE agent_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    encrypted_api_key BYTEA NOT NULL,
    model_override TEXT,
    extra_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, project_id, provider)
);

CREATE TABLE sandbox_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    generation_id UUID NOT NULL REFERENCES generations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_provider TEXT NOT NULL,
    agent_model TEXT,
    sandbox_provider TEXT NOT NULL DEFAULT 'e2b',
    sandbox_external_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'provisioning', 'running', 'validating', 'completed', 'failed', 'cancelled', 'timed_out')),
    agent_log TEXT,
    files_changed JSONB NOT NULL DEFAULT '[]'::jsonb,
    test_results JSONB NOT NULL DEFAULT '{}'::jsonb,
    validation_results JSONB NOT NULL DEFAULT '{}'::jsonb,
    tokens_used INT,
    cost_usd NUMERIC(10,6),
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sandbox_sessions_generation_id
    ON sandbox_sessions (generation_id);

CREATE INDEX idx_sandbox_sessions_project_status
    ON sandbox_sessions (project_id, status);

CREATE INDEX idx_agent_configs_org_id
    ON agent_configs (org_id);

ALTER TABLE generations
    ADD COLUMN sandbox_session_id UUID REFERENCES sandbox_sessions(id),
    ADD COLUMN agent_provider TEXT,
    ADD COLUMN agent_model TEXT;
```

### 6.2 Migration 000017_agent_codegen.down.sql

```sql
-- 000017_agent_codegen.down.sql

ALTER TABLE generations
    DROP COLUMN IF EXISTS sandbox_session_id,
    DROP COLUMN IF EXISTS agent_provider,
    DROP COLUMN IF EXISTS agent_model;

DROP TABLE IF EXISTS sandbox_sessions;
DROP TABLE IF EXISTS agent_configs;
```

## 7. Worker Pipeline

### 7.1 New Job Args

```go
package jobs

import (
    "encoding/json"

    "github.com/google/uuid"
)

type PrepareContextJobArgs struct {
    SpecID       uuid.UUID `json:"spec_id"`
    ProjectID    uuid.UUID `json:"project_id"`
    GenerationID uuid.UUID `json:"generation_id"`
    RunID        uuid.UUID `json:"run_id"`
    TaskID       uuid.UUID `json:"task_id"`
}

func (PrepareContextJobArgs) Kind() string { return "prepare_context" }

type ProvisionSandboxJobArgs struct {
    SpecID         uuid.UUID       `json:"spec_id"`
    ProjectID      uuid.UUID       `json:"project_id"`
    GenerationID   uuid.UUID       `json:"generation_id"`
    RunID          uuid.UUID       `json:"run_id"`
    TaskID         uuid.UUID       `json:"task_id"`
    ContextPayload json.RawMessage `json:"context_payload"`
}

func (ProvisionSandboxJobArgs) Kind() string { return "provision_sandbox" }

type RunAgentJobArgs struct {
    SpecID       uuid.UUID `json:"spec_id"`
    ProjectID    uuid.UUID `json:"project_id"`
    GenerationID uuid.UUID `json:"generation_id"`
    RunID        uuid.UUID `json:"run_id"`
    TaskID       uuid.UUID `json:"task_id"`
    SandboxID    string    `json:"sandbox_id"`
    SessionID    uuid.UUID `json:"session_id"`
}

func (RunAgentJobArgs) Kind() string { return "run_agent" }

type ValidateOutputJobArgs struct {
    SpecID       uuid.UUID `json:"spec_id"`
    ProjectID    uuid.UUID `json:"project_id"`
    GenerationID uuid.UUID `json:"generation_id"`
    RunID        uuid.UUID `json:"run_id"`
    TaskID       uuid.UUID `json:"task_id"`
    SandboxID    string    `json:"sandbox_id"`
    SessionID    uuid.UUID `json:"session_id"`
    RetryCount   int       `json:"retry_count"`
}

func (ValidateOutputJobArgs) Kind() string { return "validate_output" }
```

### 7.2 New Workers

- **PrepareContextWorker**: builds rich context and stores it in pipeline metadata for downstream workers.
- **ProvisionSandboxWorker**: creates sandbox, clones repo, and writes instructions/context files.
- **RunAgentWorker**: launches agent CLI, streams output, and waits for completion.
- **ValidateOutputWorker**: runs test/lint/typecheck commands and retries failures by re-enqueueing `run_agent`.

### 7.3 Pipeline Helper

```go
func CreateAgentCodegenPipeline(
    ctx context.Context,
    tx pgx.Tx,
    deps PipelineDeps,
    args CreatePipelineArgs,
) (pipelineRunID uuid.UUID, err error)
```

Creates a `pipeline_run` with ordered tasks:
1. `prepare_context`
2. `provision_sandbox`
3. `run_agent`
4. `validate_output`
5. `create_pr`
6. `notify`

### 7.4 Legacy Fallback

- If `GetEffectiveConfig(...)` returns `nil`, use existing `CreateCodegenPipeline`.
- Otherwise use `CreateAgentCodegenPipeline`.

## 8. API Endpoints

### 8.1 Agent Config Routes

```go
// org routes
r.Route("/orgs/{orgID}", func(r chi.Router) {
    r.Get("/agent-config", h.GetOrgAgentConfig)
    r.Put("/agent-config", h.UpsertOrgAgentConfig)
})

// project routes
r.Route("/projects/{projectID}", func(r chi.Router) {
    r.Get("/agent-config", h.GetProjectAgentConfig)
    r.Put("/agent-config", h.UpsertProjectAgentConfig)
    r.Delete("/agent-config", h.DeleteProjectAgentConfig)
    r.Post("/agent-config/validate", h.ValidateProjectAgentConfig)
})

// public route
r.Get("/api/v1/agent-providers", h.ListAgentProviders)
```

The providers endpoint lists available providers plus install instructions.

### 8.2 Sandbox Session Routes

Under project routes:

```go
r.Route("/projects/{projectID}", func(r chi.Router) {
    r.Get("/sessions", h.ListSandboxSessions)
    r.Get("/sessions/{sessionId}", h.GetSandboxSession)
    r.Delete("/sessions/{sessionId}", h.StopSandboxSession)
})
```

### 8.3 WebSocket Endpoint

- `GET /sessions/{sessionId}/ws` upgrades to WebSocket.
- Streams `ProgressEvent` JSON frames.
- Bidirectional channel: server sends events, client can send intervention messages.

### 8.4 Modified Endpoints

`POST /candidates/{cId}/generate` accepts optional body:

```json
{
  "agent_provider": "...",
  "agent_model": "..."
}
```

Falls back to legacy flow if no effective agent config exists.

## 9. Frontend Changes

### 9.1 Agent Config Settings Page

New route:
- `/{orgSlug}/projects/[id]/settings/agent`

Components:
- `ProviderSelect`
- `APIKeyInput`
- `ModelOverride`
- `TestConnectionButton`

TanStack Query hooks:
- `useAgentConfig`
- `useSetAgentConfig`
- `useValidateAgentConfig`
- `useAgentProviders`

### 9.2 Live Session Viewer

New component: `SessionViewer.svelte`

Features:
- terminal-style log display
- status badges
- file change sidebar
- test results
- stop/retry buttons
- WebSocket-based real-time updates

### 9.3 Generation Flow Updates

Generation page updates include:
- provider/model shown in UI
- progress stepper: `Preparing > Provisioning > Running Agent > Validating > Creating PR`
- inline live session viewer

## 10. Security

- AES-256-GCM encryption for API keys at rest.
- `ENCRYPTION_KEY` env var (32-byte hex).
- Keys decrypted only in worker; never sent to frontend or logs.
- Sandbox network outbound-only to approved LLM API domains.
- Session timeout configurable (default 20 min, hard max 60 min).
- Concurrent session limit per org (default 5).
- Audit log entry for each sandbox session.
- 30-day log retention with pruning.

## 11. Configuration

New `Config` fields:
- `E2BAPIKey` (`E2B_API_KEY`)
- `SandboxProvider` (`SANDBOX_PROVIDER`: `e2b`/`docker`/`local`, default `e2b`)
- `SandboxTimeoutMinutes` (`SANDBOX_TIMEOUT_MINUTES`, default `20`)
- `SandboxMaxRetries` (`SANDBOX_MAX_RETRIES`, default `3`)
- `EncryptionKey` (`ENCRYPTION_KEY`)
- `SandboxMaxConcurrentPerOrg` (`SANDBOX_MAX_CONCURRENT`, default `5`)
- `SandboxE2BTemplate` (`SANDBOX_E2B_TEMPLATE`, default `neuco-codegen`)

## 12. Package Structure

```text
internal/codegen/
├── provider.go                    # Provider interfaces, run options, progress events
├── provider_registry.go           # Provider registration and lookup
├── provider_claude.go             # Claude Code provider implementation
├── provider_codex.go              # OpenAI Codex provider implementation
├── provider_gemini.go             # Gemini CLI provider implementation
├── provider_opencode.go           # OpenCode provider implementation
├── provider_slate.go              # Slate CLI provider implementation
├── provider_aider.go              # Aider provider implementation
├── provider_generic.go            # Generic command-based provider
├── sandbox.go                     # Sandbox manager interfaces and core types
├── sandbox_e2b.go                 # E2B sandbox implementation
├── sandbox_local.go               # Local/Docker sandbox fallback implementation
├── context_builder.go             # Rich context assembly from spec + repo signals
├── conventions_builder.go         # CONVENTIONS.md auto-generation from repository patterns
├── instructions_builder.go        # INSTRUCTIONS.md generation
├── session_manager.go             # Session state + event fanout orchestration
├── websocket.go                   # WebSocket session stream handlers/adapters
├── encryption.go                  # AES-256-GCM encryption helpers for agent keys
├── validator.go                   # Validation runner (test/lint/typecheck)
├── pipeline.go                    # Agent codegen pipeline creation and task wiring
├── worker_prepare_context.go      # River worker for prepare_context
├── worker_provision_sandbox.go    # River worker for provision_sandbox
├── worker_run_agent.go            # River worker for run_agent
├── worker_validate_output.go      # River worker for validate_output + retries
├── store_agent_config.go          # Agent config database access
├── store_sandbox_session.go       # Sandbox session database access
└── types.go                       # Shared DTOs and internal type definitions
```

## 13. Implementation Phases

### Phase 1: Foundation (Week 1-2)
- [ ] Define provider interfaces and common abstractions
- [ ] Implement Claude Code provider baseline
- [ ] Add DB migration + store layer for configs/sessions
- [ ] Add API endpoints for agent config management
- [ ] Implement encryption key handling and API key encryption

### Phase 2: Sandbox Infrastructure (Week 2-3)
- [ ] Implement sandbox manager abstraction
- [ ] Build E2B provider implementation
- [ ] Build local/docker fallback implementation
- [ ] Implement repository clone/bootstrap flow
- [ ] Implement context builder and instruction materialization

### Phase 3: Worker Pipeline (Week 3-4)
- [ ] Add new River job args and workers
- [ ] Implement `CreateAgentCodegenPipeline`
- [ ] Persist context/session/task metadata across stages
- [ ] Add validation loop + retry mechanics
- [ ] Wire v2 pipeline selection from generation endpoint

### Phase 4: Streaming & Frontend (Week 4-5)
- [ ] Add session WebSocket endpoint and event protocol
- [ ] Build agent settings page UI and hooks
- [ ] Build live `SessionViewer.svelte`
- [ ] Update generation UI with stepper + provider info
- [ ] Add stop/retry UX + backend controls

### Phase 5: Additional Providers (Week 5-6)
- [ ] Add Codex provider
- [ ] Add Gemini provider
- [ ] Add OpenCode provider
- [ ] Add Slate provider
- [ ] Add Aider and Generic providers

### Phase 6: Production Hardening (Week 6-7)
- [ ] Harden Docker/sandbox runtime lifecycle
- [ ] Improve retry logic and failure classification
- [ ] Add concurrency controls and rate limiting
- [ ] Add audit coverage and structured error handling
- [ ] Expand integration/load testing

### Phase 7: Advanced Features (Week 8+)
- [ ] Multi-agent coordination mode
- [ ] Parallel sandbox sessions for branch exploration
- [ ] MCP server integration
- [ ] Provider analytics and cost/performance reporting
- [ ] Adaptive orchestration based on historical outcomes

## 14. Migration Strategy

- v1 legacy codegen remains available as **Quick Generate** fallback.
- v2 activates when an effective agent config exists.
- No data migration required (new columns are nullable/additive).
- Frontend shows **Configure Agent** CTA when no config exists.
- Rollback plan: drop new tables and columns via `000017_agent_codegen.down.sql`.

## 15. Success Metrics

Track these metrics:
- PR merge rate per provider
- First-pass test/validation rate
- Average retries per session
- Time-to-PR
- Cost per generation
- Session abandonment rate
- Provider comparison dashboard