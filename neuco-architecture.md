# Neuco — Technical Architecture

**Version:** 0.3  
**Status:** Draft  
**Last Updated:** March 2026

---

## 1. System Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│                            External Sources                              │
│    Gong · Intercom · Linear · Jira · Slack · HubSpot · Notion · CSV     │
└───────────────────────────────┬──────────────────────────────────────────┘
                                │  Webhooks / polling via Make.com
                                ▼
┌──────────────────────────────────────────────────────────────────────────┐
│                         neuco-api  (Go · Chi)                            │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │              Core API  (REST + SSE · Chi · JWT)                  │   │
│   │  /signals  /candidates  /specs  /generations  /webhooks          │   │
│   │  /pipelines  (customer visibility)                               │   │
│   └──────────────────────────┬───────────────────────────────────────┘   │
│                              │  transactional job insertion via River    │
└──────────────────────────────┼───────────────────────────────────────────┘
                               │
          ┌────────────────────┼───────────────────────┐
          ▼                    ▼                        ▼
┌──────────────────┐  ┌──────────────────────┐  ┌────────────────────────┐
│   PostgreSQL     │  │  neuco-worker  (Go)  │  │   Neuco Frontend       │
│   + pgvector     │  │                      │  │   (SvelteKit)          │
│                  │  │  River Pro Client    │  │   TanStack Query       │
│  app tables      │  │  ┌────────────────┐  │  │   autogen hooks        │
│  river_job       │  │  │  IngestWorker  │  │  │                        │
│  river_workflow  │  │  │  (RLM agent)   │  │  │  /dashboard            │
│  river_queue     │  │  ├────────────────┤  │  │  /signals              │
│  river_leader    │  │  │SynthesisWorker │  │  │  /candidates           │
│                  │  │  ├────────────────┤  │  │  /spec/:id             │
│                  │  │  │ CodeGenWorker  │  │  │  /pipelines            │
│                  │  │  │  (DAG workflow)│  │  │  /settings             │
│                  │  │  └────────────────┘  │  └────────────────────────┘
│                  │  │                      │
│                  │  │  Eino AI Graphs      │
│                  │  │  Cron Scheduler      │
└──────────────────┘  └──────────────────────┘
         ▲                     ▲
         └─────────────────────┘
              River reads/writes
              same Postgres DB
```

### Two deployable Go binaries, one shared codebase

| Binary | Role | Scaling |
|--------|------|---------|
| `neuco-api` | HTTP server — handles requests, inserts River jobs, streams SSE, serves pipeline UI data | Horizontal (stateless) |
| `neuco-worker` | Job processor — runs Eino graphs, LLM calls, GitHub ops via River Pro | Vertical first, then horizontal |

Both binaries live in the same Go module under `cmd/`, sharing all `internal/` packages. PostgreSQL is the single coordination layer. No Redis, no message broker, no additional services.

---

## 2. Why River + River Pro (not Temporal)

The worker layer uses **River** (open source, Postgres-backed Go job queue) and **River Pro** (paid extension adding DAG workflows, sequences, concurrency limits, and River UI) rather than a heavier orchestrator like Temporal.

| Concern | River + River Pro | Temporal |
|---------|-----------------|----------|
| Infrastructure | Zero new services — same Postgres RDS | Requires Temporal server cluster (5+ processes), Cassandra or separate PG |
| Go idioms | Built by Go engineers for Go; generics-native | Requires replacing Go stdlib primitives (channels, select) with Temporal wrappers |
| Workflow model | DAGs — right fit for Neuco's linear/branching pipelines | Full durable execution replay — more power than needed |
| Visibility UI | River UI ships as embeddable `http.Handler`, mountable in neuco-api | Separate Temporal Web UI service to deploy and maintain |
| Per-step retry | Yes — each DAG task has its own retry policy | Yes |
| Customer stats | All data in Postgres — queryable directly | Requires Temporal's visibility API or separate Elasticsearch |
| Cost | River Pro subscription (flat rate) | Temporal Cloud pricing based on action volume |
| Operational burden | ~0 (same DB you already operate) | High — Temporal is itself a distributed system |

Neuco's workflow shapes — ingest → embed → cluster → name → write, and fetch_spec → index_repo → build_context → generate → create_pr — are linear DAGs with optional fan-out. River Pro's workflow engine models these exactly. True mid-function durable execution (Temporal's main value-add) isn't needed here because each step is a discrete, idempotent unit of work.

---

## 3. Repository Structure

```
neuco/
├── cmd/
│   ├── server/
│   │   └── main.go              # API binary entrypoint
│   └── worker/
│       └── main.go              # Worker binary entrypoint
├── internal/
│   ├── api/
│   │   ├── router.go            # Chi router setup
│   │   ├── middleware/
│   │   │   ├── auth.go          # JWT validation
│   │   │   ├── tenant.go        # project-scoped request isolation
│   │   │   ├── ratelimit.go     # per-project rate limiting
│   │   │   └── logger.go
│   │   └── handlers/
│   │       ├── signals.go
│   │       ├── candidates.go
│   │       ├── specs.go
│   │       ├── generations.go
│   │       ├── projects.go
│   │       ├── pipelines.go     # customer-facing pipeline visibility
│   │       ├── sse.go           # SSE stream for live generation progress
│   │       └── webhooks.go      # Make.com inbound
│   ├── jobs/
│   │   ├── ingest.go            # IngestJobArgs + IngestWorker (River Worker impl)
│   │   ├── synthesis.go         # SynthesisJobArgs + SynthesisWorker
│   │   ├── codegen.go           # CodeGenJobArgs + CodeGenWorker (River Pro workflow)
│   │   ├── digest.go            # DigestJobArgs + DigestWorker
│   │   └── workflows/
│   │       ├── codegen_workflow.go   # River Pro DAG definition for code generation
│   │       └── synthesis_workflow.go # River Pro DAG definition for synthesis
│   ├── ai/
│   │   ├── eino.go              # Eino client init, provider wiring
│   │   ├── graphs/
│   │   │   ├── ingest_graph.go       # RLM transcript agent graph
│   │   │   ├── synthesis_graph.go    # signals → themes → candidates
│   │   │   └── codegen_graph.go      # spec + codebase → files
│   │   ├── agents/
│   │   │   ├── transcript_agent.go   # ReAct agent for RLM processing
│   │   │   └── spec_agent.go
│   │   ├── tools/
│   │   │   ├── peek.go               # RLM: peek at transcript slice
│   │   │   ├── search.go             # RLM: regex search transcript
│   │   │   ├── sub_query.go          # RLM: sub-LLM call on excerpt
│   │   │   └── emit_signal.go        # RLM: write extracted signal to DB
│   │   └── embedder.go
│   ├── ingestion/
│   │   ├── service.go
│   │   └── normalizer.go
│   ├── synthesis/
│   │   ├── service.go
│   │   ├── clusterer.go         # pgvector k-means (pure Go)
│   │   └── scorer.go
│   ├── generation/
│   │   ├── service.go
│   │   ├── indexer.go           # GitHub repo component indexer
│   │   ├── output_parser.go
│   │   └── github.go
│   ├── domain/
│   │   ├── signal.go
│   │   ├── project.go
│   │   ├── candidate.go
│   │   ├── spec.go
│   │   └── generation.go
│   └── store/
│       ├── postgres.go
│       ├── signals.go
│       ├── candidates.go
│       ├── specs.go
│       ├── generations.go
│       └── pipelines.go         # queries over river_job / river_workflow for customer UI
├── migrations/
│   └── *.sql                    # golang-migrate compatible (includes River migrations)
├── scripts/
│   └── gen_types.go             # Go structs → TypeScript types + Zod
├── Makefile
└── go.mod
```

Note: `internal/queue/` and `internal/worker/pool.go` from the previous design are **gone**. River replaces both entirely.

---

## 4. Key Libraries

| Purpose | Library |
|---------|---------|
| HTTP router | `github.com/go-chi/chi/v5` |
| Database | `github.com/jackc/pgx/v5` |
| Migrations | `github.com/golang-migrate/migrate/v4` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| Config | `github.com/spf13/viper` |
| Logging | `log/slog` (stdlib, Go 1.21+) |
| **Job queue (open source)** | **`github.com/riverqueue/river`** |
| **Workflow DAGs + UI** | **`riverqueue.com/riverpro`** (paid subscription) |
| River Postgres driver | `github.com/riverqueue/river/riverdriver/riverpgxv5` |
| River Pro Postgres driver | `riverqueue.com/riverpro/driver/riverpropgxv5` |
| LLM orchestration + agents | `github.com/cloudwego/eino` |
| Eino Anthropic provider | `github.com/cloudwego/eino-ext` |
| GitHub API | `github.com/google/go-github/v60` |
| Type generation | Custom `gen_types.go` → `.d.ts` + Zod consumed by FE |

**Why Chi over GoFr:** GoFr is well-designed for standard CRUD microservices but its opinionated response model conflicts with Neuco's SSE streaming (which requires direct `http.ResponseWriter` control). Chi is minimal and composable.

**Why Eino over LangChainGo:** Eino is Go-idiomatic by design — not a Python port. Built by ByteDance's CloudWeGo team for production agentic workloads, with first-class ReAct agent, graph composition, and multi-agent coordination. LangChainGo mirrors Python abstractions that don't map naturally to Go's concurrency model.

---

## 5. River + River Pro: Job Execution Layer

### 5.1 How River Works

River stores all job state in PostgreSQL using `SELECT ... FOR UPDATE SKIP LOCKED` for dequeuing — the same battle-tested pattern as the previous hand-rolled queue, but production-hardened with leadership election, maintenance services, exactly-once semantics, and a full UI.

Jobs are defined as strongly-typed struct pairs:

```go
// A job's arguments — serialized to/from JSON in river_job table
type IngestJobArgs struct {
    ProjectID  uuid.UUID       `json:"project_id"`
    RawPayload json.RawMessage `json:"raw_payload"`
    Source     string          `json:"source"`
}

// The kind string uniquely identifies the job type in the DB
func (IngestJobArgs) Kind() string { return "ingest" }

// The worker that processes this job type
type IngestWorker struct {
    river.WorkerDefaults[IngestJobArgs]
    eino  *ai.EinoClient
    store *store.Store
}

func (w *IngestWorker) Work(ctx context.Context, job *river.Job[IngestJobArgs]) error {
    graph := ai.BuildIngestGraph(w.eino, w.store)
    return graph.Run(ctx, &ai.IngestInput{
        ProjectID:  job.Args.ProjectID,
        RawPayload: job.Args.RawPayload,
        Source:     job.Args.Source,
    })
}
```

### 5.2 River Pro Workflows (DAGs)

River Pro's workflow engine lets multiple jobs be composed into a DAG where each task only starts when its dependencies complete. This gives Neuco per-step retry, parallel fan-out, and full visibility into multi-step pipelines.

**Code generation workflow** (the most critical user-facing pipeline):

```go
// internal/jobs/workflows/codegen_workflow.go

func NewCodeGenWorkflow(client *riverpro.Client[pgx.Tx], specID, projectID uuid.UUID) (*riverpro.WorkflowT[pgx.Tx], error) {
    wf := client.NewWorkflow(&riverpro.WorkflowOpts{
        // Embed project context in workflow metadata for visibility queries
        Metadata: mustJSON(map[string]string{
            "type":       "codegen",
            "project_id": projectID.String(),
            "spec_id":    specID.String(),
        }),
    })

    fetchSpec := wf.Add("fetch_spec",
        FetchSpecJobArgs{SpecID: specID},
        &river.InsertOpts{Queue: "codegen", Priority: 1},
        nil, // no dependencies
    )

    indexRepo := wf.Add("index_repo",
        IndexRepoJobArgs{SpecID: specID},
        &river.InsertOpts{Queue: "codegen", Priority: 1},
        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{fetchSpec}},
    )

    buildContext := wf.Add("build_context",
        BuildContextJobArgs{SpecID: specID},
        &river.InsertOpts{Queue: "codegen", Priority: 1},
        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{indexRepo}},
    )

    generateCode := wf.Add("generate_code",
        GenerateCodeJobArgs{SpecID: specID},
        &river.InsertOpts{Queue: "codegen", Priority: 1},
        &riverpro.WorkflowTaskOpts{
            Deps: []riverpro.WorkflowTask{buildContext},
            // Codegen is expensive — only retry once before surfacing to user
            river.InsertOpts{MaxAttempts: 2},
        },
    )

    createPR := wf.Add("create_pr",
        CreatePRJobArgs{SpecID: specID, ProjectID: projectID},
        &river.InsertOpts{Queue: "codegen"},
        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{generateCode}},
    )

    _ = wf.Add("notify",
        NotifyJobArgs{ProjectID: projectID, SpecID: specID},
        &river.InsertOpts{Queue: "default"},
        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{createPR}},
    )

    return wf, nil
}
```

**Synthesis workflow** (background, weekly + on-demand):

```go
func NewSynthesisWorkflow(client *riverpro.Client[pgx.Tx], projectID uuid.UUID) (*riverpro.WorkflowT[pgx.Tx], error) {
    wf := client.NewWorkflow(&riverpro.WorkflowOpts{
        Metadata: mustJSON(map[string]string{
            "type":       "synthesis",
            "project_id": projectID.String(),
        }),
    })

    fetchSignals  := wf.Add("fetch_signals",  FetchSignalsJobArgs{ProjectID: projectID}, nil, nil)
    embedMissing  := wf.Add("embed_missing",  EmbedMissingJobArgs{ProjectID: projectID}, nil,
                        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{fetchSignals}})
    clusterThemes := wf.Add("cluster_themes", ClusterThemesJobArgs{ProjectID: projectID}, nil,
                        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{embedMissing}})
    nameThemes    := wf.Add("name_themes",    NameThemesJobArgs{ProjectID: projectID}, nil,
                        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{clusterThemes}})
    scoreCandidates := wf.Add("score_candidates", ScoreCandidatesJobArgs{ProjectID: projectID}, nil,
                        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{nameThemes}})
    _ = wf.Add("write_candidates", WriteCandidatesJobArgs{ProjectID: projectID}, nil,
                        &riverpro.WorkflowTaskOpts{Deps: []riverpro.WorkflowTask{scoreCandidates}})

    return wf, nil
}
```

The key durability benefit: if `name_themes` fails, only that task retries. `fetch_signals`, `embed_missing`, and `cluster_themes` remain complete and are not re-executed.

### 5.3 Worker Binary Entrypoint

```go
// cmd/worker/main.go
func main() {
    cfg   := config.Load()
    db    := store.NewPool(cfg.DatabaseURL)
    eino  := ai.NewEinoClient(cfg)
    store := store.New(db)
    gh    := generation.NewGitHubService(cfg.GitHub)

    // Register all workers
    workers := river.NewWorkers()
    river.AddWorker(workers, jobs.NewIngestWorker(eino, store))
    river.AddWorker(workers, jobs.NewFetchSpecWorker(store))
    river.AddWorker(workers, jobs.NewIndexRepoWorker(store, gh))
    river.AddWorker(workers, jobs.NewBuildContextWorker(store))
    river.AddWorker(workers, jobs.NewGenerateCodeWorker(eino, store))
    river.AddWorker(workers, jobs.NewCreatePRWorker(store, gh))
    river.AddWorker(workers, jobs.NewNotifyWorker(store))
    river.AddWorker(workers, jobs.NewFetchSignalsWorker(store))
    river.AddWorker(workers, jobs.NewEmbedMissingWorker(eino, store))
    river.AddWorker(workers, jobs.NewClusterThemesWorker(store))
    river.AddWorker(workers, jobs.NewNameThemesWorker(eino, store))
    river.AddWorker(workers, jobs.NewScoreCandidatesWorker(store))
    river.AddWorker(workers, jobs.NewWriteCandidatesWorker(store))
    river.AddWorker(workers, jobs.NewDigestWorker(eino, store))

    riverClient, err := riverpro.NewClient(riverpropgxv5.New(db), &riverpro.Config{
        Config: river.Config{
            Queues: map[string]river.QueueConfig{
                // codegen: user is watching, cap at 3 (LLM API rate limits)
                "codegen":   {MaxWorkers: 3},
                // ingest: transcript processing, moderate LLM usage
                "ingest":    {MaxWorkers: 5},
                // synthesis: heavy clustering + Sonnet calls
                "synthesis": {MaxWorkers: 2},
                // default: notifications, low-cost ops
                "default":   {MaxWorkers: 10},
            },
            Workers: workers,
            // Periodic jobs managed by River (replaces hand-rolled cron)
            PeriodicJobs: []*river.PeriodicJob{
                river.NewPeriodicJob(
                    river.ScheduleCron("0 8 * * 1"), // Monday 8am UTC
                    func() (river.JobArgs, *river.InsertOpts) {
                        return DigestAllProjectsJobArgs{}, nil
                    },
                    &river.PeriodicJobOpts{RunOnStart: false},
                ),
            },
        },
    })
    if err != nil {
        slog.Error("failed to create river client", "error", err)
        os.Exit(1)
    }

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    if err := riverClient.Start(ctx); err != nil {
        slog.Error("failed to start river client", "error", err)
        os.Exit(1)
    }

    <-ctx.Done()
    slog.Info("worker shutting down, draining in-flight jobs...")
    // River's Stop waits for all in-flight jobs to complete before returning
    if err := riverClient.Stop(ctx); err != nil {
        slog.Error("error stopping river client", "error", err)
    }
    slog.Info("worker stopped cleanly")
}
```

### 5.4 Transactional Job Insertion (API side)

River's core guarantee: jobs inserted in the same database transaction as the triggering write are atomically coupled. If the transaction rolls back, the job disappears. This eliminates the whole class of "webhook was received but ingest job was never created" bugs.

```go
// internal/api/handlers/webhooks.go
func (h *WebhookHandler) HandleMake(w http.ResponseWriter, r *http.Request) {
    projectID := chi.URLParam(r, "projectId")
    secret    := chi.URLParam(r, "secret")

    if !h.store.ValidateWebhookSecret(r.Context(), projectID, secret) {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }

    var payload ingestion.RawWebhookPayload
    json.NewDecoder(r.Body).Decode(&payload)

    // Single transaction: write the raw signal record AND enqueue the ingest job.
    // If anything fails, both roll back atomically.
    err := pgx.BeginTxFunc(r.Context(), h.db, pgx.TxOptions{}, func(tx pgx.Tx) error {
        rawID, err := h.store.InsertRawSignalTx(r.Context(), tx, projectID, payload)
        if err != nil {
            return err
        }
        _, err = h.riverClient.InsertTx(r.Context(), tx, jobs.IngestJobArgs{
            ProjectID:  uuid.MustParse(projectID),
            RawSignalID: rawID,
            Source:     payload.Source,
        }, &river.InsertOpts{Queue: "ingest"})
        return err
    })
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusAccepted)
}
```

### 5.5 Retry Policy per Job Type

River supports per-job retry configuration. Neuco uses this to tune retry behavior to each step's cost profile:

```go
// Expensive LLM calls: limited retries, longer backoff
type GenerateCodeJobArgs struct { SpecID uuid.UUID `json:"spec_id"` }
func (GenerateCodeJobArgs) Kind() string { return "generate_code" }
func (w *GenerateCodeWorker) NextRetry(job *river.Job[GenerateCodeJobArgs]) time.Time {
    // Retry after 2 minutes, then 10 minutes — give rate limits time to clear
    backoffs := []time.Duration{2 * time.Minute, 10 * time.Minute}
    if job.Attempt <= len(backoffs) {
        return time.Now().Add(backoffs[job.Attempt-1])
    }
    return time.Now().Add(30 * time.Minute)
}

// Cheap idempotent ops: fast exponential backoff
type EmbedMissingJobArgs struct { ProjectID uuid.UUID `json:"project_id"` }
func (EmbedMissingJobArgs) Kind() string { return "embed_missing" }
// Uses River's default exponential backoff (no override needed)
```

---

## 6. Eino AI Layer

### 6.1 Provider Setup

```go
// internal/ai/eino.go
type EinoClient struct {
    Sonnet   model.ChatModel  // claude-sonnet-4-6  — spec gen, code gen
    Haiku    model.ChatModel  // claude-haiku-4-5   — theme naming, sub-queries
    Embedder *Embedder        // text-embedding-3-small
}

func NewEinoClient(cfg *Config) *EinoClient {
    sonnet, _ := einoanthropic.NewChatModel(ctx, &einoanthropic.ChatModelConfig{
        Model:  "claude-sonnet-4-6",
        APIKey: cfg.AnthropicAPIKey,
    })
    haiku, _ := einoanthropic.NewChatModel(ctx, &einoanthropic.ChatModelConfig{
        Model:  "claude-haiku-4-5-20251001",
        APIKey: cfg.AnthropicAPIKey,
    })
    return &EinoClient{Sonnet: sonnet, Haiku: haiku, Embedder: NewEmbedder(cfg.OpenAIKey)}
}
```

### 6.2 Ingest Graph — RLM Transcript Agent

Rather than naively chunking and embedding entire transcripts, each long-form document is processed by a **ReAct agent** implementing the Recursive Language Model (RLM) pattern. The transcript is stored as a Go variable; the agent uses tools to explore it programmatically.

```
Transcript arrives (raw text, up to 80k tokens)
    │
    │  Stored as Go string — NOT sent wholesale to LLM
    ▼
Eino ReAct Agent  (Haiku — cheap sub-queries)
    ├── peek(start, end int) string        → read lines start–end
    ├── search(pattern string) []string   → regex search full transcript
    ├── sub_query(question, excerpt) str  → focused Haiku call on excerpt only
    └── emit_signal(content, type, meta) → write signal to DB via store
    │
    │  Agent iterates until done or maxSteps (40)
    ▼
8–15 discrete, structured signals in DB
Each < 500 tokens, semantically sharp, ready for embedding
```

Token efficiency: a 60-min Gong call (~40k tokens) produces 8–15 signals using ~4–8k total tokens. Naive full-transcript embedding costs ~40k tokens and produces one unstructured blob that degrades clustering quality downstream.

```go
// internal/ai/graphs/ingest_graph.go
func BuildIngestGraph(eino *EinoClient, store *store.Store) *graph.Graph {
    return graph.NewGraph(
        graph.WithNode("agent", agents.NewReActAgent(
            agents.WithModel(eino.Haiku),
            agents.WithTools([]tools.Tool{
                tools.NewPeekTool(),
                tools.NewSearchTool(),
                tools.NewSubQueryTool(eino.Haiku),
                tools.NewEmitSignalTool(store),
            }),
            agents.WithMaxSteps(40),
            agents.WithSystemPrompt(transcriptAgentPrompt),
        )),
    )
}
```

**System prompt:**
```
You are a product signal extractor. Read a call transcript or support thread and
extract discrete product signals.

The full transcript is available in memory. Use your tools to explore it:
- peek(start, end) — read a section by line numbers
- search(pattern) — regex search for keywords across the full text
- sub_query(question, excerpt) — ask a focused question about a small excerpt
- emit_signal(content, type, metadata) — record a signal when you find one

Signal types: feature_request | pain_point | praise | bug_report | question

A good signal is specific, grounded in the speaker's words, and actionable.

Strategy:
1. Peek at the opening and closing (often most signal-dense)
2. Search for: "wish", "want", "can't", "need", "broken", "love", "hate", "always"
3. For each hit, sub_query to get full context, then emit_signal if it qualifies
4. Declare done when you've covered the document
```

### 6.3 Synthesis Graph

```
FetchSignalsNode     → pulls recent unprocessed signals from DB
    ↓
EmbedMissingNode     → ensures all signals have embeddings (parallel, batched 100)
    ↓
ClusterNode          → pgvector k-means (pure Go, no LLM)
    ↓
ThemeNameNode        → Haiku call per cluster: name + problem summary
    ↓
ScorerNode           → frequency × recency × segment weight
    ↓
CandidateWriterNode  → upserts feature_candidates in DB
```

Each of these maps directly to a River Pro workflow task. When Eino's graph runs inside a River worker, each node's output is persisted to the DB before the next task begins — so failures resume from the last completed node.

### 6.4 Code Generation Graph

```
FetchSpecNode        → loads spec + candidate + supporting signals
    ↓
IndexRepoNode        → fetches repo component tree via GitHub API (cached in S3)
    ↓
BuildContextNode     → selects top-N similar components + stories as examples
    ↓
GenerateCodeNode     → Sonnet call with full context → structured JSON output
    ↓
ParseOutputNode      → validates file paths, strips markdown fences
    ↓
CreatePRNode         → creates branch + commits + opens draft PR
    ↓
NotifyNode           → writes completion event, updates generation record
```

GenerateCodeNode uses Eino's structured output mode to guarantee valid JSON:

```go
graph.WithNode("codegen", agents.NewStructuredAgent(
    agents.WithModel(eino.Sonnet),
    agents.WithOutputSchema(CodeGenOutputSchema),
    agents.WithSystemPrompt(codegenSystemPrompt),
    agents.WithMaxTokens(8192),
))
```

The codegen system prompt includes the user's framework, styling approach, existing component patterns, and design tokens extracted from the codebase index. This is what makes generated code fit the existing project.

### 6.5 Model Usage and Cost

| Operation | Model | Est. tokens | Est. cost |
|-----------|-------|-------------|-----------|
| Signal extraction (1 transcript, RLM) | `claude-haiku-4-5` | ~5k | ~$0.003 |
| Batch embedding (100 signals) | `text-embedding-3-small` | ~50k | ~$0.002 |
| Theme naming (1 cluster) | `claude-haiku-4-5` | ~2k | ~$0.001 |
| Spec generation | `claude-sonnet-4-6` | ~8k | ~$0.05 |
| Code generation | `claude-sonnet-4-6` | ~16k | ~$0.10–0.30 |

---

## 7. API Server (`neuco-api`)

### 7.1 Binary Entrypoint

```go
// cmd/server/main.go
func main() {
    cfg := config.Load()
    db  := store.NewPool(cfg.DatabaseURL)

    // API server creates a River client for inserting jobs only (no workers)
    riverClient, _ := riverpro.NewClient(riverpropgxv5.New(db), &riverpro.Config{
        Config: river.Config{
            // No Queues configured = insert-only mode (no workers started)
            Workers: river.NewWorkers(),
        },
    })

    r := chi.NewRouter()
    r.Use(middleware.Logger())
    r.Use(middleware.Recoverer())
    r.Use(middleware.Timeout(30 * time.Second))
    r.Use(appMiddleware.CORS(cfg))
    r.Use(appMiddleware.Auth(cfg.JWTSecret))

    deps := &api.Deps{Store: store.New(db), River: riverClient, Config: cfg, DB: db}
    api.Mount(r, deps)

    // Mount River UI for internal ops visibility (auth-gated)
    riverUI, _ := riverui.NewServer(&riverui.ServerOpts{Client: riverClient, DB: db})
    r.With(appMiddleware.InternalOnly).Mount("/internal/river", riverUI)

    srv := &http.Server{Addr: ":" + cfg.Port, Handler: r}
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()
    go srv.ListenAndServe()
    <-ctx.Done()
    srv.Shutdown(context.Background())
}
```

The API server has no Eino dependency. Handlers validate input, write to DB or insert River jobs, and return immediately. No LLM calls in the request path.

### 7.2 Pipeline Visibility Handler

The `pipelines.go` handler exposes customer-facing pipeline data by querying River's tables directly:

```go
// internal/api/handlers/pipelines.go

// GET /api/v1/projects/:id/pipelines
// Returns all workflow runs for a project with per-task status
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
    projectID := r.Context().Value(middleware.ProjectIDKey).(uuid.UUID)
    pipelines, err := h.store.ListProjectPipelines(r.Context(), projectID)
    // ...
    render.JSON(w, r, pipelines)
}

// GET /api/v1/projects/:id/pipelines/:workflowId
// Returns a single workflow with full per-task detail
func (h *PipelineHandler) GetPipeline(w http.ResponseWriter, r *http.Request) {
    workflowID := chi.URLParam(r, "workflowId")
    pipeline, err := h.store.GetPipelineDetail(r.Context(), workflowID)
    // ...
    render.JSON(w, r, pipeline)
}

// POST /api/v1/projects/:id/pipelines/:workflowId/retry
// Retries only the failed tasks in a workflow
func (h *PipelineHandler) RetryPipeline(w http.ResponseWriter, r *http.Request) {
    workflowID := chi.URLParam(r, "workflowId")
    _, err := h.riverClient.WorkflowRetry(r.Context(), workflowID, nil)
    // ...
}
```

```go
// internal/store/pipelines.go
// Queries River's tables to build customer-facing pipeline summaries

func (s *Store) ListProjectPipelines(ctx context.Context, projectID uuid.UUID) ([]PipelineSummary, error) {
    const sql = `
        SELECT
            rj.metadata->>'workflow_id'              AS workflow_id,
            rj.metadata->>'type'                     AS pipeline_type,
            COUNT(*)                                 AS total_tasks,
            COUNT(*) FILTER (WHERE rj.state = 'completed')   AS completed_tasks,
            COUNT(*) FILTER (WHERE rj.state = 'running')     AS running_tasks,
            COUNT(*) FILTER (WHERE rj.state = 'available')   AS pending_tasks,
            COUNT(*) FILTER (WHERE rj.state IN ('retryable', 'discarded')) AS failed_tasks,
            MIN(rj.created_at)                       AS started_at,
            MAX(rj.finalized_at)                     AS finished_at,
            EXTRACT(EPOCH FROM (MAX(rj.finalized_at) - MIN(rj.created_at)))
                                                     AS duration_seconds
        FROM river_job rj
        WHERE rj.metadata->>'project_id' = $1
          AND rj.metadata->>'workflow_id' IS NOT NULL
        GROUP BY rj.metadata->>'workflow_id', rj.metadata->>'type'
        ORDER BY started_at DESC
        LIMIT 50`
    // ...
}

func (s *Store) GetProjectStats(ctx context.Context, projectID uuid.UUID) (*ProjectStats, error) {
    const sql = `
        SELECT
            COUNT(*) FILTER (WHERE metadata->>'type' = 'codegen'
                             AND state = 'completed')   AS prs_created,
            COUNT(*) FILTER (WHERE state = 'discarded') AS failed_jobs,
            COUNT(*)                                    AS total_jobs,
            AVG(
                EXTRACT(EPOCH FROM (finalized_at - created_at))
            ) FILTER (WHERE metadata->>'type' = 'codegen'
                      AND state = 'completed')          AS avg_codegen_seconds
        FROM river_job
        WHERE metadata->>'project_id' = $1
          AND created_at > NOW() - INTERVAL '30 days'`
    // ...
}
```

### 7.3 SSE Progress Stream

For the code generation live view, the SSE handler polls River's job state directly:

```go
// internal/api/handlers/sse.go
func (h *SSEHandler) StreamGeneration(w http.ResponseWriter, r *http.Request) {
    genID   := chi.URLParam(r, "gId")
    flusher := w.(http.Flusher)
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")

    ticker := time.NewTicker(750 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-r.Context().Done():
            return
        case <-ticker.C:
            // Query River job states for all tasks in this generation's workflow
            tasks, _ := h.store.GetWorkflowTaskStates(r.Context(), genID)
            data, _ := json.Marshal(tasks)
            fmt.Fprintf(w, "data: %s\n\n", data)
            flusher.Flush()

            if allDone(tasks) {
                fmt.Fprintf(w, "event: done\ndata: {}\n\n")
                flusher.Flush()
                return
            }
        }
    }
}
```

### 7.4 Full API Endpoint List

All endpoints prefixed `/api/v1/`, authenticated via Bearer JWT.

```
POST   /api/v1/auth/github/callback
GET    /api/v1/auth/me

GET    /api/v1/projects
POST   /api/v1/projects
GET    /api/v1/projects/:id
PATCH  /api/v1/projects/:id

POST   /api/v1/projects/:id/signals/upload      # CSV/text → inserts ingest job
GET    /api/v1/projects/:id/signals             # paginated, filtered
DELETE /api/v1/projects/:id/signals/:signalId

GET    /api/v1/projects/:id/candidates
POST   /api/v1/projects/:id/candidates/refresh  # inserts synthesis workflow
PATCH  /api/v1/projects/:id/candidates/:cId

GET    /api/v1/projects/:id/candidates/:cId/spec
POST   /api/v1/projects/:id/candidates/:cId/spec/generate
PATCH  /api/v1/projects/:id/candidates/:cId/spec

POST   /api/v1/projects/:id/candidates/:cId/generate  # inserts codegen workflow
GET    /api/v1/projects/:id/generations
GET    /api/v1/projects/:id/generations/:gId
GET    /api/v1/projects/:id/generations/:gId/stream   # SSE progress

# Customer-facing pipeline visibility
GET    /api/v1/projects/:id/pipelines                 # list all workflow runs
GET    /api/v1/projects/:id/pipelines/:workflowId     # detail + per-task state
POST   /api/v1/projects/:id/pipelines/:workflowId/retry  # retry failed tasks
GET    /api/v1/projects/:id/stats                     # aggregate stats for dashboard

POST   /api/v1/webhooks/make/:projectId/:secret  # Make.com inbound → ingest job

GET    /api/v1/projects/:id/integrations
POST   /api/v1/projects/:id/integrations
DELETE /api/v1/projects/:id/integrations/:iId

# Internal ops only (auth: internal token or admin JWT)
GET    /internal/river/*   # River UI (embedded http.Handler)
```

---

## 8. Frontend (SvelteKit)

### 8.1 Project Structure

```
neuco-web/
├── src/
│   ├── lib/
│   │   ├── api/
│   │   │   ├── types.gen.ts         # auto-generated from Go domain structs
│   │   │   ├── client.ts
│   │   │   └── hooks/
│   │   │       ├── useProjects.ts
│   │   │       ├── useCandidates.ts
│   │   │       ├── useSignals.ts
│   │   │       ├── useSpec.ts
│   │   │       ├── useGeneration.ts
│   │   │       ├── usePipelines.ts   # pipeline list + detail
│   │   │       ├── useProjectStats.ts
│   │   │       └── useJobStream.ts   # SSE hook for live generation progress
│   │   ├── components/
│   │   │   ├── signals/
│   │   │   ├── candidates/
│   │   │   ├── spec/
│   │   │   ├── generation/
│   │   │   │   └── ProgressStream.svelte
│   │   │   ├── pipelines/
│   │   │   │   ├── PipelineList.svelte    # paginated list of workflow runs
│   │   │   │   ├── PipelineDetail.svelte  # DAG visualisation + per-task status
│   │   │   │   └── PipelineStats.svelte   # aggregate stats cards
│   │   │   └── ui/
│   │   └── stores/
│   │       └── project.ts
│   └── routes/
│       ├── +layout.svelte
│       ├── (auth)/login/
│       └── (app)/
│           ├── dashboard/           # stats + recent pipeline activity
│           ├── signals/
│           ├── candidates/[id]/spec/
│           ├── candidates/[id]/generate/
│           ├── pipelines/           # full pipeline activity feed
│           │   └── [workflowId]/    # per-workflow DAG view
│           └── settings/integrations/
└── scripts/
    └── gen_hooks.ts
```

### 8.2 Pipeline Detail View

The `/pipelines/[workflowId]` route renders an interactive DAG of the workflow tasks, sourced from River's job state. The visualization mirrors what River UI shows internally but is scoped to the customer's project and styled to Neuco's design system.

Task node states map directly to River job states:

| River state | UI display |
|-------------|-----------|
| `pending` / `scheduled` | Queued (grey) |
| `running` | In progress (blue, animated) |
| `completed` | Done (green + duration) |
| `retryable` | Retrying (amber + attempt count) |
| `discarded` | Failed (red + error message) |

---

## 9. Database Schema

River Pro runs its own migrations (`river migrate-up --line pro`) which create the `river_job`, `river_workflow`, `river_queue`, `river_leader`, and related tables. Neuco's app tables reference River's tables by the `workflow_id` stored in River's job metadata.

**Application tables:**

```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    github_id    TEXT UNIQUE NOT NULL,
    github_login TEXT NOT NULL,
    email        TEXT,
    avatar_url   TEXT,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE projects (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    github_repo TEXT,
    framework   TEXT NOT NULL DEFAULT 'react',
    styling     TEXT NOT NULL DEFAULT 'tailwind',
    owner_id    UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE signals (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    source_ref  TEXT,
    type        TEXT NOT NULL,
    content     TEXT NOT NULL,
    metadata    JSONB DEFAULT '{}',
    occurred_at TIMESTAMPTZ,
    ingested_at TIMESTAMPTZ DEFAULT NOW(),
    embedding   vector(1536)
);
CREATE INDEX signals_project_idx ON signals(project_id, ingested_at DESC);
-- HNSW index: better query performance than IVFFlat, no training step required
CREATE INDEX signals_embedding_idx ON signals USING hnsw (embedding vector_cosine_ops);

CREATE TABLE feature_candidates (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    problem_summary TEXT,
    signal_count    INT DEFAULT 0,
    score           FLOAT DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'new',
    suggested_at    TIMESTAMPTZ DEFAULT NOW(),
    centroid        vector(1536)
);

CREATE TABLE candidate_signals (
    candidate_id UUID REFERENCES feature_candidates(id) ON DELETE CASCADE,
    signal_id    UUID REFERENCES signals(id) ON DELETE CASCADE,
    relevance    FLOAT,
    PRIMARY KEY (candidate_id, signal_id)
);

CREATE TABLE specs (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    candidate_id        UUID NOT NULL REFERENCES feature_candidates(id) ON DELETE CASCADE,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    problem_statement   TEXT,
    proposed_solution   TEXT,
    user_stories        JSONB DEFAULT '[]',
    acceptance_criteria JSONB DEFAULT '[]',
    out_of_scope        JSONB DEFAULT '[]',
    ui_changes          TEXT,
    data_model_changes  TEXT,
    open_questions      JSONB DEFAULT '[]',
    version             INT DEFAULT 1,
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE generations (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    spec_id      UUID NOT NULL REFERENCES specs(id),
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- workflow_id references River's river_workflow table via metadata
    workflow_id  TEXT,
    status       TEXT NOT NULL DEFAULT 'pending',
    branch_name  TEXT,
    pr_url       TEXT,
    pr_number    INT,
    files        JSONB DEFAULT '[]',
    error        TEXT,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX generations_workflow_idx ON generations(workflow_id);

CREATE TABLE integrations (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id     UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider       TEXT NOT NULL,
    webhook_secret TEXT,
    config         JSONB DEFAULT '{}',
    last_sync_at   TIMESTAMPTZ,
    is_active      BOOLEAN DEFAULT TRUE,
    created_at     TIMESTAMPTZ DEFAULT NOW()
);
```

**River's own tables (created by `river migrate-up --line pro`):**

| Table | Purpose |
|-------|---------|
| `river_job` | All job state — args, status, attempts, errors, timing, metadata |
| `river_workflow` | Workflow identity and aggregate state |
| `river_queue` | Queue configuration and stats |
| `river_leader` | Leader election for maintenance operations |
| `river_job_dead_letter` | Dead-lettered jobs (exhausted retries) |

Neuco's pipeline visibility queries run against `river_job` filtered by `metadata->>'project_id'` — no separate event store needed.

---

## 10. Infrastructure

### 10.1 Services

| Service | Provider | Notes |
|---------|----------|-------|
| `neuco-api` | AWS ECS Fargate | Stateless; horizontal scale behind ALB |
| `neuco-worker` | AWS ECS Fargate | Separate task definition; scale independently |
| Database | AWS RDS PostgreSQL 16 | db.t4g.medium; pgvector, uuid-ossp, River tables all on same instance |
| File storage | S3 | CSV uploads, repo index cache keyed by `{repo}:{commit_sha}` |
| CDN | CloudFront | Frontend static assets |
| Secrets | AWS Secrets Manager | API keys, JWT secret, GitHub credentials |
| Frontend | Vercel | SvelteKit adapter-vercel |

No Redis, no SQS, no Elasticsearch. River + Postgres handles all async work and job visibility.

### 10.2 Worker Scaling Path

```
v1  (<50 projects)    Single worker ECS task, River handles concurrency via MaxWorkers per queue
v2  (50–500)          2–3 worker tasks; River uses pg leadership election to coordinate
v3  (500+)            Separate ECS services per queue (codegen vs ingest vs synthesis)
                      Monitor via River's built-in queue stats + CloudWatch alarms on queue depth
```

River's leadership election ensures maintenance jobs (job cleanup, stuck job detection) run on exactly one worker instance regardless of how many are deployed — no additional coordination needed.

### 10.3 Environment Variables

```env
# Shared by both binaries
DATABASE_URL=postgres://...
OPENAI_API_KEY=
ANTHROPIC_API_KEY=
AWS_S3_BUCKET=
AWS_REGION=

# API only
PORT=8080
JWT_SECRET=
GITHUB_CLIENT_ID=
GITHUB_CLIENT_SECRET=
INTERNAL_API_TOKEN=        # for /internal/river UI auth

# Worker only
GITHUB_APP_PRIVATE_KEY=

# Make.com
MAKE_WEBHOOK_SIGNING_SECRET=
```

### 10.4 Dockerfile (multi-stage, multi-target)

```dockerfile
FROM golang:1.24-alpine AS base
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM base AS server-build
RUN go build -o /bin/neuco-api ./cmd/server

FROM base AS worker-build
RUN go build -o /bin/neuco-worker ./cmd/worker

FROM alpine:latest AS server
COPY --from=server-build /bin/neuco-api /bin/neuco-api
ENTRYPOINT ["/bin/neuco-api"]

FROM alpine:latest AS worker
COPY --from=worker-build /bin/neuco-worker /bin/neuco-worker
ENTRYPOINT ["/bin/neuco-worker"]
```

### 10.5 River Migrations in CI/CD

River Pro ships its own migration lines. These run as part of the deploy pipeline alongside Neuco's application migrations:

```
make migrate-up:
    # Run Neuco's own migrations
    migrate -path migrations -database $DATABASE_URL up
    # Run River's base migration line
    river migrate-up --database-url $DATABASE_URL
    # Run River Pro's additional migration line
    river migrate-up --database-url $DATABASE_URL --line pro
```

### 10.6 CI/CD

```
push to main
    ↓
GitHub Actions
    ├── go test ./... -race
    ├── make gen  (fail if generated files have diff)
    ├── docker build --target server  → push neuco-api:sha to ECR
    ├── docker build --target worker  → push neuco-worker:sha to ECR
    └── ECS rolling deploy (api + worker task definitions independently)
           ↓ on new deploy
       make migrate-up (runs once per deploy via ECS task override)
```

---

## 11. Security

- All DB queries enforced through `tenant.go` middleware — every handler receives a project-scoped context, impossible to cross-query tenant data
- Pipeline visibility endpoints (`/pipelines`, `/stats`) filter by `project_id` from the authenticated JWT — customers can only see their own workflows
- River UI (`/internal/river`) is gated by `InternalOnly` middleware — requires a separate `INTERNAL_API_TOKEN`, never exposed to customers
- GitHub tokens encrypted at rest in Secrets Manager, never in the database
- Make.com webhook secrets are 32-byte random per integration, validated with constant-time comparison on every inbound request
- JWT: 24h access token, 30d refresh
- Generation endpoint rate-limited: 10 per hour per project (LLM cost control)
- Neuco never executes generated code — writes it to a draft PR only

---

## 12. Local Development

```bash
# Prerequisites: Go 1.24+, Node 20+, Docker

docker compose up -d postgres   # postgres:16 with pgvector pre-enabled

# Run all migrations (app + River base + River Pro)
make migrate-up

# Generate TS types + TanStack hooks
make gen

make run-api      # Terminal 1: API server (air hot reload)
make run-worker   # Terminal 2: Worker (air hot reload)

cd web && npm install && npm run dev   # Terminal 3: SvelteKit dev server

make test         # go test ./... -race -count=1
```

`docker-compose.yml` services: `postgres:16` with pgvector extension, `mailhog` (email testing), optional `n8n` container for local integration testing.

### Makefile

```makefile
gen:
    go run scripts/gen_types.go
    npx tsx scripts/gen_hooks.ts

run-api:
    air -c .air.api.toml

run-worker:
    air -c .air.worker.toml

migrate-up:
    migrate -path migrations -database $$DATABASE_URL up
    river migrate-up --database-url $$DATABASE_URL
    river migrate-up --database-url $$DATABASE_URL --line pro

migrate-down:
    river migrate-down --database-url $$DATABASE_URL --line pro
    river migrate-down --database-url $$DATABASE_URL
    migrate -path migrations -database $$DATABASE_URL down 1

test:
    go test ./... -race -count=1

build:
    go build ./cmd/server
    go build ./cmd/worker
```

---

## 13. Future Considerations

- **Queue backend:** River is Postgres-native and has no SQS migration path — this is a feature, not a limitation. If RDS ever becomes a bottleneck for job throughput specifically, the path is vertical scaling of the RDS instance before considering a queue migration. River has been benchmarked at tens of thousands of jobs per second on a capable Postgres instance.
- **Per-queue worker services:** When ingest and codegen have divergent scaling curves, split `neuco-worker` into `neuco-worker-ingest` and `neuco-worker-codegen` listening on different River queues. River's leadership election handles the coordination automatically.
- **pgvector → pgvectorscale:** HNSW handles tens of millions of vectors comfortably. At 50M+ signals per project, evaluate Timescale's pgvectorscale (DiskANN implementation) — benchmarks at significantly lower p95 latency at equivalent recall.
- **RLM fine-tuning:** Once 1,000+ transcripts have been processed through the ingest agent, fine-tune a smaller model specifically for signal extraction. Target ~80% cost reduction on ingest while maintaining extraction quality.
- **Repo index caching:** Store the indexed component tree in S3 as `{repo}:{commit_sha}.json`. Invalidate on GitHub push webhook. Eliminates re-indexing on every generation for the same commit.
- **Customer pipeline webhooks:** Once the pipeline visibility layer is mature, expose outbound webhooks so customers can receive pipeline completion/failure events in their own tooling (Slack, PagerDuty, etc.).
