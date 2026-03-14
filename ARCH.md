# Neuco — Architecture Overview

## 1. What is Neuco?

Neuco is an AI-native product intelligence platform:

- Ingests customer/product signals (calls, tickets, Slack, Linear/Jira, webhooks, CSV, etc.)
- Synthesizes those signals into feature candidates
- Generates product specs and code
- Opens GitHub PRs for implementation

At a high level, Neuco combines:

1. **Multi-tenant SaaS backend** (Go + PostgreSQL + pgvector)
2. **Asynchronous AI/automation pipelines** (River jobs)
3. **SaaS frontend** (SvelteKit)

---

## 2. System Architecture

```text
                           ┌──────────────────────────┐
                           │        Neuco Web         │
                           │  SvelteKit on Vercel     │
                           └────────────┬─────────────┘
                                        │ HTTPS + JWT + refresh cookie
                                        ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                          Neuco API (Go / Chi)                               │
│ - Auth (GitHub/Google), org/project APIs, SSE, webhooks, operator routes   │
│ - Inserts River jobs (insert-only usage in API process)                     │
└───────────────────────────────┬──────────────────────────────────────────────┘
                                │ shared PostgreSQL (app data + River tables)
                                ▼
                     ┌────────────────────────────┐
                     │   PostgreSQL 16 + pgvector│
                     │   (Neon in cloud)          │
                     └──────────────┬─────────────┘
                                    │
                                    ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                        Neuco Worker (Go / River)                            │
│ - Polls 4 queues: ingest/synthesis/codegen/default                          │
│ - Runs pipelines: ingest, synthesis, spec_gen, codegen, sync, digest, etc. │
└──────────────────────────────────────────────────────────────────────────────┘

External APIs:
- Anthropic + OpenAI (LLM/chat/embeddings)
- GitHub OAuth + GitHub App (auth + repo/PR ops)
- Stripe (billing)
- Nango + Intercom + Slack + Linear + Jira integrations
- Resend (email)
- Sentry + PostHog (observability)
```

### Key architecture notes

- **Two Go binaries from one module** (`cmd/server`, `cmd/worker` in `github.com/neuco-ai/neuco`)
- **No separate message broker** for jobs: River uses PostgreSQL tables
- **Redis appears only for local Nango in docker-compose**, not Neuco job orchestration

---

## 3. Repository Structure

> Based on actual repo contents (`internal/` depth 2, `neuco-web/src/` depth 3, `migrations/` depth 1).

```text
.
├── cmd/
│   ├── server/main.go                 # API process bootstrap
│   └── worker/main.go                 # Worker process bootstrap
├── internal/
│   ├── ai/
│   │   ├── agents/transcript_agent.go # ReAct transcript extractor
│   │   ├── client.go                  # Anthropic/OpenAI client + retries + breaker
│   │   ├── circuitbreaker.go
│   │   └── query.go                   # semantic signal query engine
│   ├── api/
│   │   ├── handlers/                  # route handlers
│   │   ├── middleware/                # auth/RBAC/tenant/rate-limit/Sentry/CORS
│   │   ├── deps.go
│   │   └── router.go
│   ├── config/config.go               # env config via Viper
│   ├── domain/                        # entity types + enums
│   ├── email/                         # email service/templates
│   ├── generation/                    # repo indexing + context + GitHub service
│   ├── intercom/ jira/ linear/ slack/ # native integration clients
│   ├── jobs/                          # River workers, args, pipeline helpers
│   ├── nango/                         # Nango client/sync service
│   ├── observability/                 # logging + sentry init
│   └── store/                         # persistence layer by aggregate
├── migrations/
│   ├── 000001_initial_schema.up.sql
│   ├── ...
│   └── 000016_expand_signal_types.up.sql
├── neuco-web/
│   ├── package.json
│   └── src/
│       ├── lib/
│       │   ├── api/
│       │   │   ├── client.ts          # fetch wrapper + key transforms + refresh
│       │   │   ├── queries/           # TanStack query hooks
│       │   │   └── types.gen.ts
│       │   ├── analytics.ts           # PostHog wrappers
│       │   ├── stores/auth.svelte.ts
│       │   └── components/ui/         # design system components
│       ├── routes/
│       │   ├── (app)/[orgSlug]/...    # authenticated app routes
│       │   ├── auth/ login/ onboarding/
│       │   └── operator/...           # operator pages
│       ├── hooks.client.ts            # Sentry + PostHog init
│       └── hooks.server.ts            # Sentry init
├── .github/workflows/
│   ├── ci.yml
│   ├── deploy.yml
│   ├── preview-deploy.yml
│   └── preview-cleanup.yml
├── docker-compose.yml
├── Dockerfile
├── fly.api.toml
├── fly.worker.toml
├── DECISIONS.md
└── Makefile
```

---

## 4. Backend Architecture

### 4.1 HTTP Layer (Chi router, middleware stack, SSE)

**Entry point:** `cmd/server/main.go`

- Loads config (`internal/config.Load`)
- Initializes logging + Sentry
- Connects `pgxpool`
- Builds `store.Store`
- Builds River client (insert mode, workers registered for known kinds)
- Constructs deps (`internal/api.NewDeps`) and router (`internal/api.NewRouter`)
- Starts `http.Server` with:
  - `ReadTimeout: 15s`
  - `WriteTimeout: 60s` (explicitly for SSE)
  - `IdleTimeout: 120s`

**Global middleware stack** (`internal/api/router.go`):

1. `RealIP`
2. `RequestID`
3. `SentryContext`
4. `SentryRecovery`
5. `CORS(frontendURL)`
6. `RequestLogger`

**Security middleware highlights** (`internal/api/middleware/*`):

- JWT auth: `Authenticate(jwtSecret)` (`HS256`, claims include `user_id`, `org_id`, `role`)
- RBAC: `RequireRole(owner/admin/member/viewer ranking)`
- Tenant isolation: `ProjectTenant` verifies `{projectId}` belongs to JWT org
- Operator auth: `InternalToken` (constant-time static bearer token)
- Billing/usage gates: `RequireActiveSubscription`, `CheckSignalLimit`, `CheckProjectLimit`, `CheckPRLimit`
- Rate limits + body-size caps on auth/webhooks/default routes

**SSE endpoint:**

- `GET /api/v1/projects/{projectId}/generations/{gId}/stream`
- Sends `progress` events every 750ms, final `done` when generation reaches completed/failed

### 4.2 Domain Model (entity relationships diagram)

```text
User ──< OrgMember >── Organization ──< Project
                              │             │
                              │             ├──< Signal >──(vector embedding)
                              │             │      │
                              │             │      └──< CandidateSignal >── FeatureCandidate ──< Spec
                              │             │                                        │              │
                              │             │                                        │              └──< Generation >── GitHub PR
                              │             │                                        │
                              │             │                                        └── CopilotNote
                              │             │
                              │             ├──< PipelineRun ──< PipelineTask
                              │             ├──< Integration
                              │             └──< ProjectContext
                              │
                              ├──< Subscription / Usage
                              ├──< Notification
                              └──< AuditLog
```

Primary domain types referenced:

- `internal/domain/organization.go` (plans, org roles, tenant boundary)
- `internal/domain/project.go`
- `internal/domain/signal.go`
- `internal/domain/pipeline.go`
- `internal/domain/user.go`

### 4.3 Database Layer (Store pattern, pgvector, tenant isolation)

**Store pattern** (`internal/store/store.go`):

- Single `Store` wrapping `*pgxpool.Pool`
- Methods split by concern (`signals.go`, `pipelines.go`, `projects.go`, etc.)
- Shared pagination helpers (`Page`)
- Transaction helper `withTx`

**Schema & pgvector**

- Initial migration enables `vector` extension (`CREATE EXTENSION IF NOT EXISTS vector`)
- `signals.embedding` is `vector(1536)`
- HNSW ANN index:
  - `ON signals USING hnsw (embedding vector_cosine_ops)`
  - `WITH (ef_construction = 200, m = 16)`

**Semantic query path**

- `SignalQueryEngine` (`internal/ai/query.go`) embeds natural-language question via OpenAI embedding model
- Calls `Store.SearchSignalsByEmbedding` (`internal/store/signals_search.go`)
- SQL uses `embedding <=> $queryVector::vector` (cosine distance) with optional filters (source/type/time)

**Tenant isolation**

- Enforced at multiple layers:
  - JWT carries org context
  - middleware verifies project belongs to org (`ProjectTenant`)
  - scoped store methods (e.g., `GetProject(ctx, orgID, projectID)` style patterns)
  - org/project IDs present in nearly all key tables

### 4.4 API Route Map (from `internal/api/router.go`)

#### Public utility

- `GET /health`
- `GET /healthz`
- `GET /ready`
- `GET /readyz`
- `GET /docs`
- `GET /docs/openapi.yaml`

#### Auth (`/api/v1/auth`)

- `POST /github/callback`
- `POST /google/callback`
- `POST /refresh`
- `POST /logout`
- Authenticated subgroup:
  - `GET /me`
  - `GET /github/repos`
  - `POST /nango/connect-session`

#### Webhooks (`/api/v1/webhooks`)

- `POST /{projectId}/{secret}`
- `POST /stripe`
- `POST /intercom`
- `POST /slack`
- `POST /linear`
- `POST /jira`

#### Onboarding

- `GET /api/v1/onboarding/status`
- `POST /api/v1/onboarding/step`
- `POST /api/v1/onboarding/skip`

#### Orgs (`/api/v1/orgs`)

- `GET /`
- `POST /`
- `/{orgId}`:
  - `GET /`
  - `PATCH /` (admin)
  - `GET /members`
  - `PUT /members/me/digest`
  - `POST /members/invite` (admin)
  - `PATCH /members/{userId}` (owner)
  - `DELETE /members/{userId}` (admin)
  - `GET /projects`
  - `POST /projects` (member + project limit)
  - `/github`
    - `POST /installations` (admin)
    - `GET /repos`
  - `/billing`
    - `GET /subscription`
    - `GET /usage`
    - `POST /checkout` (admin)
    - `POST /portal` (admin)
  - `GET /analytics`
  - `GET /llm-usage`
  - `/notifications`
    - `GET /`
    - `GET /unread-count`
    - `PATCH /{notificationId}/read`
    - `POST /read-all`
  - `GET /audit-log`

#### Projects (`/api/v1/projects/{projectId}`)

- `GET /`
- `PATCH /` (admin)
- `DELETE /` (admin)

Signals:

- `GET /signals`
- `POST /signals/upload` (signal limit)
- `POST /signals/query`
- `DELETE /signals/{signalId}`

Candidates/spec/codegen:

- `GET /candidates`
- `POST /candidates/refresh`
- `PATCH /candidates/{cId}`
- `GET /candidates/{cId}/spec`
- `PATCH /candidates/{cId}/spec`
- `POST /candidates/{cId}/spec/generate`
- `POST /candidates/{cId}/generate` (generation rate limit + PR limit)

Generations:

- `GET /generations`
- `GET /generations/{gId}`
- `GET /generations/{gId}/stream`

Pipelines:

- `GET /pipelines`
- `GET /pipelines/{runId}`
- `POST /pipelines/{runId}/retry`

Stats / usage:

- `GET /stats`
- `GET /llm-usage`
- `GET /llm-usage/calls`
- `GET /pipelines/{runId}/llm-usage`

Project context:

- `GET /contexts`
- `POST /contexts`
- `POST /contexts/search`
- `GET /contexts/{contextId}`
- `PATCH /contexts/{contextId}`
- `DELETE /contexts/{contextId}`

Copilot notes:

- `GET /copilot/notes`
- `PATCH /copilot/notes/{noteId}`

Integrations:

- `GET /integrations`
- `POST /integrations` (admin)
- `GET /integrations/{integrationId}`
- `DELETE /integrations/{integrationId}` (admin)

Native integration flows:

- Intercom: `/intercom/authorize`, `/intercom/callback`, `/intercom/{integrationId}/sync`, delete
- Slack: `/slack/authorize`, `/slack/callback`, `/slack/{integrationId}/sync`, delete
- Linear: `/linear/authorize`, `/linear/callback`, `/linear/{integrationId}/sync`, delete
- Jira: `/jira/authorize`, `/jira/callback`, `/jira/{integrationId}/sync`, delete

Nango-managed:

- `GET /nango/connections`
- `POST /nango/connections` (admin)
- `DELETE /nango/connections/{connectionId}` (admin)
- `POST /nango/sync/{connectionId}`

#### Operator (`/operator`, internal token)

- `GET /orgs`
- `GET /orgs/{orgId}`
- `GET /users`
- `GET /health`
- `GET /flags`
- `PATCH /flags/{key}`

---

## 5. Worker Architecture

### 5.1 Job Queue (River config, 4 queues)

`cmd/worker/main.go` configures River with PostgreSQL driver and queues:

| Queue | MaxWorkers | Purpose |
|---|---:|---|
| `ingest` | 5 | Ingestion + embedding pipeline steps |
| `synthesis` | 2 | Candidate synthesis and scoring |
| `codegen` | 3 | Spec->context->code->PR chain |
| `default` | 10 | Notifications, copilot, sync, periodic jobs |

Workers are registered centrally in `internal/jobs/registry.go`.

### 5.2 Pipelines (task chains)

Pipeline metadata tracked using custom tables (`pipeline_runs`, `pipeline_tasks`) and helper constructors in `internal/jobs/pipeline_helpers.go`.

| Pipeline type | Tasks (ordered) |
|---|---|
| `ingest` | `ingest` → `embed` |
| `synthesis` | `fetch_signals` → `embed_missing` → `cluster_themes` → `name_themes` → `score_candidates` → `write_candidates` → `update_context` |
| `spec_gen` | `generate_spec` |
| `codegen` | `fetch_spec` → `index_repo` → `build_context` → `generate_code` → `create_pr` → `notify` |
| `nango_sync` | `nango_sync` → `embed` |
| `intercom_sync` | `intercom_sync` → `embed` |
| `slack_sync` | `slack_sync` → `embed` |
| `linear_sync` | `linear_sync` → `embed` |
| `jira_sync` | `jira_sync` → `embed` |

Task chaining is explicit in workers via `getRiverClient().Insert(...)` and queue selection (`synthesis`, `codegen`, etc.).

### 5.3 Pipeline Tracking (custom tables)

Store methods in `internal/store/pipelines.go`:

- `CreatePipelineRun`
- `CreatePipelineTask`
- `UpdatePipelineTaskStatus`
- `UpdatePipelineRunStatus`
- `GetPipelineRun` (+ tasks)
- `ListProjectPipelines`

Status helper functions (`StartTask`, `CompleteTask`, `FailTask`, `CheckPipelineCompletion`) in `internal/jobs/pipeline_helpers.go` keep task/run state synchronized and emit notifications on completion/failure.

### 5.4 Periodic Jobs

Configured directly in worker River config (`cmd/worker/main.go`):

- Weekly: `DigestAllProjectsJobArgs` on `synthesis`
- Every 6h: `SyncAllIntegrationsJobArgs` on `default`
- Weekly: `DigestEmailsJobArgs` on `default`

---

## 6. AI Layer

### 6.1 LLM Client (models, retry, circuit breaker)

`internal/ai/client.go` + `internal/ai/circuitbreaker.go` provide a unified client.

**Models/constants currently used:**

- Embeddings: `text-embedding-3-small` (1536 dims)
- Anthropic chat: `claude-sonnet-4-5`, `claude-haiku-4-5-20251001`
- Some job workers directly call Anthropic with `claude-sonnet-4-6-20250514` / Haiku

**Resilience controls:**

- Retry up to `maxRetries=5` on HTTP 429
- Truncated exponential backoff with full jitter (`retryBaseMs=500`, cap `retryMaxMs=30000`)
- Circuit breaker opens after 5 consecutive failures, 30s cooldown, then half-open
- Per-call timeout: 30s for raw API helper (`aiCallTimeout`)

### 6.2 Transcript Agent (ReAct agent tools)

`internal/ai/agents/transcript_agent.go` implements a ReAct loop for long-form transcripts:

- Max iterations: 40
- Model: `claude-sonnet-4-5`
- Tool loop uses Anthropic tool protocol (`tool_use`/`tool_result`)

Tools exposed to model:

- `peek(start,end)` line-window retrieval
- `search(pattern)` regex over full transcript
- `sub_query(question,excerpt)` focused analysis call (Haiku)
- `emit_signal(content,signal_type,metadata)` persist extracted signal

In ingestion worker (`internal/jobs/ingest.go`), content above `longFormThreshold=2000` chars routes through this agent.

### 6.3 Signal Query Engine (pgvector semantic search)

`internal/ai/query.go`:

1. Embed natural-language question
2. Execute nearest-neighbor query with optional source/type/time filters
3. Return ranked matches with cosine distance

`internal/store/signals_search.go` SQL uses `embedding <=> query_vector` and supports tenant/project scoping + filter clauses.

---

## 7. Frontend Architecture

### 7.1 Tech Stack

From `neuco-web/package.json` and source:

- **SvelteKit 2 + Svelte 5 + Vite 7 + TypeScript**
- **TanStack Query** (`@tanstack/svelte-query`) for server-state
- **Tailwind v4** + Bits UI + component abstractions
- **Sentry** (`@sentry/sveltekit`) and **PostHog** (`posthog-js`)
- **Nango frontend SDK** (`@nangohq/frontend`)

### 7.2 Routing

Key route groups in `neuco-web/src/routes`:

- Public: `/`, `/login`, `/auth/*`, `/onboarding`, `/privacy`, `/terms`
- App shell: `/(app)` with org-scoped routes under `[orgSlug]`
- Org/project pages:
  - `/{orgSlug}/dashboard`
  - `/{orgSlug}/projects`
  - `/{orgSlug}/projects/[id]/signals|candidates|generations|pipelines|integrations|memory`
  - `/{orgSlug}/settings/*`
- Operator pages:
  - `/operator`, `/operator/orgs`, `/operator/users`, `/operator/flags`, `/operator/health`

### 7.3 API Integration (fetch wrapper, key transform, token refresh)

`neuco-web/src/lib/api/client.ts` provides a centralized client:

- Base URL from `VITE_API_BASE_URL` (fallback `http://localhost:8080`)
- Adds bearer access token from localStorage
- Always sends cookies (`credentials: include`) for refresh token flow
- Proactive refresh if token expires in <5 minutes
- On 401: attempts silent refresh (`POST /api/v1/auth/refresh`) then retries once
- Redirects to `/login` if still unauthorized
- Recursive key transforms:
  - response: `snake_case -> camelCase`
  - request: `camelCase -> snake_case`

### 7.4 Observability (Sentry + PostHog)

- `src/hooks.client.ts`: initializes Sentry + PostHog
- `src/hooks.server.ts`: initializes Sentry server-side
- `src/lib/analytics.ts`: typed PostHog events (signup/login/project_created/signal_uploaded/spec_generated/codegen_started/pr_created etc.)

---

## 8. Infrastructure & Deployment

### 8.1 Services (Fly.io, Neon, Vercel)

- **Fly.io**:
  - `fly.api.toml` deploys API (`app=neuco-api`, port 8080, SSE-friendly response timeout)
  - `fly.worker.toml` deploys worker (`app=neuco-worker`, background VM, no HTTP service)
- **Neon**:
  - Used in preview workflow to create per-PR DB branches and run migrations
- **Vercel**:
  - Frontend deploy handled by Vercel Git integration (not in main deploy job)

### 8.2 CI/CD (4 workflows)

Workflows in `.github/workflows`:

| Workflow | Purpose |
|---|---|
| `ci.yml` | Go vet/lint/test/build + frontend check/build + migration pair validation |
| `deploy.yml` | Deploy `neuco-api` + `neuco-worker` to Fly after CI success on main |
| `preview-deploy.yml` | On PR open/update: create Neon branch, run migrations, deploy preview Fly app, comment URL |
| `preview-cleanup.yml` | On PR close: destroy preview Fly app and delete Neon branch |

### 8.3 Docker (multi-stage)

`Dockerfile` uses multi-stage targets:

1. `base` (Go 1.25-alpine, download deps, copy source)
2. `server-build` (`go build ./cmd/server`)
3. `worker-build` (`go build ./cmd/worker`)
4. runtime `server` (alpine, non-root `neuco`, healthcheck `/health`)
5. runtime `worker` (alpine, non-root `neuco`)

Both runtime targets copy `migrations/` into image.

### 8.4 Local Development

`docker-compose.yml` provides:

- `postgres` (`pgvector/pgvector:pg16`)
- `redis` + `nango-server` (for Nango local stack)
- `mailhog`
- `neuco-api` target=server
- `neuco-worker` target=worker

`Makefile` shortcuts:

- `run-api`, `run-worker` (Air)
- `migrate-up`, `migrate-down`
- `test`, `build`, `lint`
- `dev` bootstraps local Postgres and prints run instructions

---

## 9. Security Model

Core controls visible in current code:

1. **JWT auth** (HS256) with custom claims including org context and role
2. **RBAC hierarchy** (`owner > admin > member > viewer`) enforced via middleware
3. **Tenant checks**: project-scoped routes verify `projectId` belongs to JWT org
4. **Operator isolation**: `/operator/*` protected by independent static internal token
5. **Rate limiting + body-size caps** per route group (auth/webhooks/default/generation)
6. **Subscription/usage guardrails**:
   - inactive subscription -> HTTP 402 on protected project APIs
   - usage exhaustion -> HTTP 429
7. **Webhook boundaries**: separate webhook routes with dedicated limiter and request cap
8. **Constant-time token compare** for internal operator token validation

---

## 10. Key Design Decisions

From `DECISIONS.md` and reflected in implementation:

| Decision | Implementation impact |
|---|---|
| Monorepo (Go + SvelteKit) | Single module + `neuco-web/` app, unified workflows |
| Chi router | Composable middleware and direct SSE streaming support |
| River OSS (no River Pro) | Manual DAG/job chaining + custom `pipeline_runs/tasks` tables |
| Team-first tenancy (Org->Members->Projects) | JWT org context + RBAC + tenant middleware |
| GitHub App for repo operations | Codegen path uses installation ID, branch/commit/PR automation |
| SvelteKit + TanStack Query | Frontend route model + query hooks + generated types |
| Resend for email | Async email jobs (`send_email`, digest workflows) |
| pgvector + HNSW | Semantic signal retrieval, dedup and AI query features |

### Notable implementation nuance

`DECISIONS.md` says Eino was selected as framework direction, but the current codebase uses a custom internal AI client/agent implementation (`internal/ai/*`) with direct Anthropic/OpenAI HTTP calls.

---

## Appendix A — Selected concrete config values

- `internal/config/config.go` defaults:
  - `PORT=8080`
  - `FRONTEND_URL=http://localhost:5173`
  - `AWS_REGION=us-east-1`
  - `NANGO_SERVER_URL=http://localhost:3003`
  - `APP_ENV=development`
- Required validation currently enforces:
  - `DATABASE_URL`
  - `JWT_SECRET`
- Worker queue caps: ingest=5, synthesis=2, codegen=3, default=10
- API Fly app: `primary_region="iad"`, concurrency soft 200 / hard 250

## Appendix B — Example backend bootstrap flow

```go
// cmd/server/main.go (condensed)
cfg, _ := config.Load()
observability.InitLogging("neuco-api", cfg.AppEnv)
pool, _ := pgxpool.New(context.Background(), cfg.DatabaseURL)
s := store.New(pool)
workers := river.NewWorkers()
jobs.RegisterAllWorkers(workers, s, cfg)
riverClient, _ := river.NewClient(riverpgxv5.New(pool), &river.Config{Workers: workers})
handler := api.NewRouter(api.NewDeps(s, riverClient, cfg, pool), slog.Default())
```

## Appendix C — Example frontend API behavior

```ts
// neuco-web/src/lib/api/client.ts (condensed)
if (browser && isTokenExpiringSoon() && getAccessToken()) {
  await silentRefresh();
}

const token = getAccessToken();
if (token) headers['Authorization'] = `Bearer ${token}`;

// outgoing body: camelCase -> snake_case
init.body = JSON.stringify(transformKeysToSnake(body));

// incoming body: snake_case -> camelCase
return transformKeys(await response.json()) as T;
```

---

This document reflects the current implementation in the checked-in code and workflow configs, including route-level details from `internal/api/router.go`.
