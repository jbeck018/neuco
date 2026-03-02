# Neuco — Product Requirements Document

**Version:** 0.2  
**Status:** Draft  
**Last Updated:** March 2026  
**Owner:** Engineering

---

## 1. Vision

Neuco is an AI-native product intelligence platform that closes the loop between customer signals and shipped code. It answers the question every engineering-led team asks every sprint: *"What should we build next — and can we just build it?"*

Neuco ingests qualitative and quantitative signals from the tools PMs and engineers already use, synthesizes them into prioritized, evidence-backed feature specs, and outputs production-ready UI components and Storybook stories that slot directly into an existing codebase via a GitHub PR.

---

## 2. Problem Statement

The modern product development loop has three broken seams:

1. **Signal → Insight:** Customer feedback lives scattered across Gong, Intercom, Slack, and spreadsheets. Synthesizing it into actionable direction is manual, slow, and lossy.
2. **Insight → Spec:** Product specs are written in prose for human engineers. As coding agents take over implementation, the hand-off format is obsolete.
3. **Spec → Code:** Even with coding agents, someone still has to scaffold the component, set up the story states, and open the PR. This is the last mile that burns an afternoon.

Neuco eliminates all three seams.

---

## 3. Target Users

### Primary: Engineering-Led Startups (5–50 people)
- No dedicated PM, or PM-to-eng ratio of 1:5+
- Founders or lead engineers making product decisions
- Already using Cursor, Claude Code, or similar coding agents
- Pain: drowning in feedback, no structured way to act on it

### Secondary: Product Teams at Growth-Stage Companies (50–200 people)
- Dedicated PMs who want AI leverage on the discovery-to-delivery workflow
- Engineering teams tired of translating Figma mocks into component scaffolding
- Pain: too many inputs, not enough throughput

### Anti-target (v1)
- Enterprise teams with dedicated design systems teams (too much governance overhead)
- Non-SaaS products (physical products, pure content sites)

---

## 4. Core User Journey

```
1. Connect    → User connects their signal sources and GitHub repo
2. Ingest     → Neuco processes calls, tickets, and feedback through AI extraction pipelines
3. Synthesize → Weekly (or on-demand) "What should we build?" digest
4. Specify    → User picks a theme; Neuco generates a structured spec
5. Generate   → Neuco outputs components + Storybook stories in the user's framework
6. Review     → Draft PR opened in GitHub for team review
7. Merge      → Feedback on the PR feeds back into the signal layer
```

---

## 5. Feature Requirements

### 5.1 Signal Ingestion

| Source | Integration Method | Priority |
|--------|-------------------|----------|
| Gong | OAuth + Gong API (call transcripts, topics) | P0 |
| Intercom | OAuth + Intercom API (conversations, tags) | P0 |
| CSV / plain text upload | Direct upload, any format | P0 (MVP fallback) |
| Slack | Slack OAuth, channel-specific indexing | P1 |
| Linear | OAuth, issues + comments | P1 |
| Jira | OAuth, tickets + comments | P1 |
| HubSpot | OAuth, deal notes + contact activity | P1 |
| Notion | Notion OAuth, selected databases/pages | P1 |
| Salesforce | OAuth, opportunity notes, case notes | P2 |
| Mixpanel / Amplitude | API key + event export | P2 |
| GitHub Issues | GitHub OAuth | P2 |
| Zapier / Make webhook | Inbound webhook endpoint | P2 |

**Integration Strategy:** Rather than building and maintaining every connector in-house, Neuco uses **Make.com** as the integration backbone. Make handles OAuth flows, scheduling, and data normalization for PM-facing tools. Neuco exposes a single inbound webhook endpoint; Make scenarios push normalized payloads to it. This gives Neuco access to Make's 2,000+ app catalog from day one with minimal maintenance overhead. For self-hosted or enterprise customers, **n8n** is the supported alternative.

**Signal data model:**
```
Signal {
  id          uuid
  project_id  uuid
  source      enum (gong | intercom | linear | jira | hubspot | notion | salesforce | csv | slack | webhook)
  source_ref  string          // original ID in source system
  type        enum (call_transcript | support_ticket | feature_request | bug_report | review | note | event)
  content     text
  metadata    jsonb           // speaker, sentiment, tags, user_segment, etc.
  occurred_at timestamp
  ingested_at timestamp
  embedding   vector(1536)    // stored in pgvector
}
```

### 5.2 Synthesis Engine

**Weekly Digest (automated):**
- Runs every Monday 8am in the user's timezone (configurable)
- Groups signals into themes using embedding clustering
- Scores each theme by: frequency × recency × user_segment_weight × churn_risk
- Surfaces top 5 themes with representative signal excerpts and score rationale

**On-Demand Query:**
- Natural language interface: "What are churned enterprise customers asking for?"
- Filters by source, date range, user segment, signal type
- Returns ranked themes with drill-down to raw signals

**Feature Candidate Output:**
```
FeatureCandidate {
  id              uuid
  project_id      uuid
  title           string
  problem_summary text
  signal_count    int
  signals         Signal[]     // supporting evidence
  score           float
  suggested_at    timestamp
  status          enum (new | specced | in_progress | shipped | rejected)
}
```

### 5.3 Spec Generator

When a user selects a feature candidate, Neuco generates a structured spec:

**Spec fields:**
- **Problem Statement** — What user pain this addresses, grounded in signal language
- **Proposed Solution** — High-level description of the change
- **User Stories** — Standard "As a [role], I want [action] so that [outcome]" format
- **Acceptance Criteria** — Testable conditions for completion
- **Out of Scope** — Explicit exclusions to prevent scope creep
- **Suggested UI Changes** — Described in terms of components and states, not pixels
- **Data Model Changes** — Tables/fields to add or modify
- **Open Questions** — Unresolved decisions requiring team input

Specs are editable inline. Changes are tracked and versioned.

### 5.4 Code Generation Layer

This is the core differentiator. Neuco generates framework-native UI components + Storybook stories based on the spec and the user's existing codebase.

**Supported output frameworks (user-configured per project):**
- React (JSX / TSX)
- Next.js (App Router or Pages Router)
- Vue 3 (Composition API, `<script setup>`)
- Svelte / SvelteKit
- Angular (component + module)
- Solid.js

**Codebase indexing:**
- User connects their GitHub repo
- Neuco indexes: component library, design token files, existing Storybook stories, TypeScript types, CSS/styling approach (Tailwind, CSS Modules, styled-components, etc.)
- Index refreshes on push to main/trunk

**Generation output per spec:**
1. **Component file** — in the user's framework, following their naming conventions and styling approach
2. **Storybook story file** — covering Default, Loading, Error, Empty, and interaction states
3. **Types file** (if TypeScript) — props interface and any new domain types
4. **Unit test scaffold** — basic render tests using their existing test setup (Vitest, Jest, Testing Library)
5. **PR description** — auto-generated with problem statement, solution summary, and links back to source signals

**Generation is additive, not destructive.** Neuco never modifies existing files. All output is new files in a feature branch.

**PR flow:**
1. Neuco creates a feature branch: `neuco/feature-slug-YYYY-MM-DD`
2. Commits generated files
3. Opens draft PR with structured description
4. PR is assigned to the user for review before merging

### 5.5 Pipeline Activity & Visibility

Every background operation Neuco runs on behalf of a project — ingesting transcripts, synthesizing themes, generating code — is a **durable pipeline** with real-time visibility. Customers can see exactly what Neuco is doing, what it has done, and what failed.

**Per-pipeline detail view:**
- Step-by-step task breakdown (e.g. `fetch_spec → index_repo → build_context → generate_code → create_pr`)
- Live status per task: queued, running, completed, failed
- Duration for each completed step
- Error message and retry count for failed steps
- Links to outputs: generated PR URL, signals consumed, spec version used

**Project-level activity feed:**
- Chronological list of all pipeline runs for a project
- Filterable by type (ingest, synthesis, codegen) and status (running, completed, failed)
- Aggregate stats: total signals processed, PRs created, average generation time, failure rate

**Customer-facing stats (dashboard):**

| Metric | Description |
|--------|-------------|
| Signals ingested | Total and by source, last 30 days |
| Themes identified | Clusters found in last synthesis run |
| PRs created | Total generated, total merged |
| Avg. generation time | p50/p95 across all codegen runs |
| Pipeline success rate | % of runs that completed without error |
| Last synthesis | Timestamp + theme count |

These stats are derived directly from the job execution store (no separate analytics pipeline needed at v1).

**Failure transparency:**
- Failed pipelines surface in a dedicated "Needs Attention" card on the dashboard
- Each failed task shows the error message and a one-click retry button
- Dead-lettered jobs (exhausted all retries) are surfaced with a support escalation path

### 5.6 Project Memory

Neuco builds a project context graph over time:

- Accepted PR patterns inform future generation style
- Rejected/heavily-modified components feed back as negative examples
- Merged features are marked "shipped" and connected to subsequent signal changes
- Sprint retrospective mode: "Did shipping X reduce the volume of Y complaints?"

### 5.7 Integrations Hub

Central place to manage all connections. Each integration card shows:
- Connection status + last sync timestamp
- Volume of signals ingested (last 30 days)
- Enable/disable toggle
- Re-auth button

---

## 6. Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| Signal ingestion latency | < 5 minutes from source event to indexed signal |
| Synthesis query response | < 10 seconds for on-demand queries |
| Code generation time | < 60 seconds for component + story (p50) |
| Pipeline durability | Failed steps retry automatically; no pipeline is silently lost |
| Job visibility | All pipeline tasks queryable with status and duration |
| Uptime | 99.5% monthly |
| Data isolation | All project data strictly tenant-isolated |
| Auth | GitHub OAuth (required), Google SSO (P1) |

---

## 7. Out of Scope (v1)

- Figma / design output (Storybook IS the design spec)
- Real-time collaboration / multiplayer editing
- Mobile app code generation (web only)
- Automated merging (always human-reviewed)
- Custom LLM fine-tuning per customer
- Voice input / meeting recording (rely on Gong for this)

---

## 8. Success Metrics

| Metric | Target (Month 6) |
|--------|-----------------|
| Weekly active projects | 50 |
| Signal sources connected per project (median) | 3 |
| Spec-to-PR conversion rate | > 40% |
| PRs merged without major modification | > 30% |
| User-reported time saved per sprint | > 3 hours |
| Pipeline success rate | > 95% |
| MRR | $10,000 |

---

## 9. Pricing Model (Proposed)

| Tier | Price | Limits |
|------|-------|--------|
| **Starter** | $49/mo | 1 project, 3 signal sources, 10 PRs/mo |
| **Builder** | $149/mo | 3 projects, unlimited sources, 50 PRs/mo |
| **Team** | $399/mo | 10 projects, unlimited everything, priority support |
| **Enterprise** | Custom | SSO, on-prem n8n, SLA, audit logs, pipeline audit history |

---

## 10. MVP Scope (v0.1 — Target: 6 weeks)

### Must have
- CSV / plain text signal upload
- Synthesis engine (theme clustering + scoring)
- Feature candidate list view
- Spec generator
- React + Next.js code generation (Storybook stories + component)
- GitHub PR creation
- Pipeline activity feed with per-task status and duration
- Single-project, single-user (no multi-tenancy yet)

### Nice to have
- Gong integration via Make webhook
- Intercom integration via Make webhook
- Framework selector (Vue, Svelte)
- Dashboard aggregate stats (signals ingested, PRs created)

### Explicitly excluded from MVP
- Linear / Jira / HubSpot integrations
- Project memory / retrospective mode
- Team collaboration
- Billing
- On-demand pipeline retry UI (auto-retry handles it in the background)
