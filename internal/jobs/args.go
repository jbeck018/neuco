package jobs

import (
	"encoding/json"

	"github.com/google/uuid"
)

// ============================================================
// Ingest Pipeline
// ============================================================

type IngestJobArgs struct {
	ProjectID  uuid.UUID       `json:"project_id"`
	RawPayload json.RawMessage `json:"raw_payload"`
	Source     string          `json:"source"`
	RunID      uuid.UUID       `json:"run_id"`
	TaskID     uuid.UUID       `json:"task_id"`
}

func (IngestJobArgs) Kind() string { return "ingest" }

type EmbedJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	SignalIDs []uuid.UUID `json:"signal_ids"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (EmbedJobArgs) Kind() string { return "embed" }

// ============================================================
// Synthesis Pipeline
// ============================================================

type FetchSignalsJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (FetchSignalsJobArgs) Kind() string { return "fetch_signals" }

type ClusterThemesJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (ClusterThemesJobArgs) Kind() string { return "cluster_themes" }

type NameThemesJobArgs struct {
	ProjectID   uuid.UUID   `json:"project_id"`
	ClusterIDs  []uuid.UUID `json:"cluster_ids"`
	RunID       uuid.UUID   `json:"run_id"`
	TaskID      uuid.UUID   `json:"task_id"`
}

func (NameThemesJobArgs) Kind() string { return "name_themes" }

type ScoreCandidatesJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (ScoreCandidatesJobArgs) Kind() string { return "score_candidates" }

type WriteCandidatesJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (WriteCandidatesJobArgs) Kind() string { return "write_candidates" }

type UpdateContextJobArgs struct {
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (UpdateContextJobArgs) Kind() string { return "update_context" }

// ============================================================
// Spec Generation Pipeline
// ============================================================

type SpecGenJobArgs struct {
	CandidateID uuid.UUID `json:"candidate_id"`
	ProjectID   uuid.UUID `json:"project_id"`
	RunID       uuid.UUID `json:"run_id"`
	TaskID      uuid.UUID `json:"task_id"`
}

func (SpecGenJobArgs) Kind() string { return "spec_gen" }

// ============================================================
// Code Generation Pipeline
// ============================================================

type FetchSpecJobArgs struct {
	SpecID    uuid.UUID `json:"spec_id"`
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (FetchSpecJobArgs) Kind() string { return "fetch_spec" }

type IndexRepoJobArgs struct {
	SpecID    uuid.UUID `json:"spec_id"`
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

func (IndexRepoJobArgs) Kind() string { return "index_repo" }

type BuildContextJobArgs struct {
	SpecID        uuid.UUID `json:"spec_id"`
	ProjectID     uuid.UUID `json:"project_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
	// RepoIndexJSON carries the serialised generation.RepoIndex produced by
	// IndexRepoWorker. When empty, code generation proceeds without codebase
	// context.
	RepoIndexJSON string    `json:"repo_index_json,omitempty"`
}

func (BuildContextJobArgs) Kind() string { return "build_context" }

type GenerateCodeJobArgs struct {
	SpecID    uuid.UUID `json:"spec_id"`
	ProjectID uuid.UUID `json:"project_id"`
	RunID     uuid.UUID `json:"run_id"`
	TaskID    uuid.UUID `json:"task_id"`
	// CodegenContext carries the formatted few-shot context built by
	// BuildContextWorker and embedded verbatim in the LLM prompt.
	CodegenContext string `json:"codegen_context,omitempty"`
}

func (GenerateCodeJobArgs) Kind() string { return "generate_code" }

type CreatePRJobArgs struct {
	SpecID       uuid.UUID `json:"spec_id"`
	ProjectID    uuid.UUID `json:"project_id"`
	GenerationID uuid.UUID `json:"generation_id"`
	RunID        uuid.UUID `json:"run_id"`
	TaskID       uuid.UUID `json:"task_id"`
}

func (CreatePRJobArgs) Kind() string { return "create_pr" }

type NotifyJobArgs struct {
	ProjectID    uuid.UUID `json:"project_id"`
	GenerationID uuid.UUID `json:"generation_id"`
	RunID        uuid.UUID `json:"run_id"`
	TaskID       uuid.UUID `json:"task_id"`
}

func (NotifyJobArgs) Kind() string { return "notify" }

// ============================================================
// Digest
// ============================================================

type DigestAllProjectsJobArgs struct{}

func (DigestAllProjectsJobArgs) Kind() string { return "digest_all_projects" }

// DigestEmailsJobArgs triggers weekly digest emails for all orgs.
type DigestEmailsJobArgs struct{}

func (DigestEmailsJobArgs) Kind() string { return "digest_emails" }

// ============================================================
// Copilot
// ============================================================

type CopilotReviewJobArgs struct {
	ProjectID  uuid.UUID `json:"project_id"`
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id"`
	RunID      uuid.UUID `json:"run_id"`
	TaskID     uuid.UUID `json:"task_id"`
}

func (CopilotReviewJobArgs) Kind() string { return "copilot_review" }

// ============================================================
// Transactional Email
// ============================================================

// SendEmailJobArgs enqueues a transactional email for async delivery.
// EmailType determines which template to render; Payload carries the
// template-specific data as JSON.
type SendEmailJobArgs struct {
	EmailType string          `json:"email_type"` // welcome, invite, pr_created, weekly_digest
	Payload   json.RawMessage `json:"payload"`
}

func (SendEmailJobArgs) Kind() string { return "send_email" }

// ============================================================
// Nango Integration Sync
// ============================================================

// NangoSyncJobArgs carries the parameters for a Nango integration sync job.
// Provider identifies which sync method to invoke (gong, intercom, slack, …).
// IntegrationID is the Neuco integration DB record; RunID/TaskID are optional
// pipeline tracking references.
type NangoSyncJobArgs struct {
	ProjectID     uuid.UUID `json:"project_id"`
	ConnectionID  string    `json:"connection_id"`
	Provider      string    `json:"provider"`
	IntegrationID uuid.UUID `json:"integration_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
}

func (NangoSyncJobArgs) Kind() string { return "nango_sync" }

// SyncAllIntegrationsJobArgs triggers a periodic sweep that enqueues individual
// NangoSyncJobArgs for every active non-webhook integration.
type SyncAllIntegrationsJobArgs struct{}

func (SyncAllIntegrationsJobArgs) Kind() string { return "sync_all_integrations" }

// ============================================================
// Native Intercom Sync
// ============================================================

// IntercomSyncJobArgs carries the parameters for a native Intercom sync job.
type IntercomSyncJobArgs struct {
	ProjectID     uuid.UUID `json:"project_id"`
	IntegrationID uuid.UUID `json:"integration_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
}

func (IntercomSyncJobArgs) Kind() string { return "intercom_sync" }

// ============================================================
// Native Slack Sync
// ============================================================

// SlackSyncJobArgs carries the parameters for a native Slack sync job.
type SlackSyncJobArgs struct {
	ProjectID     uuid.UUID `json:"project_id"`
	IntegrationID uuid.UUID `json:"integration_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
}

func (SlackSyncJobArgs) Kind() string { return "slack_sync" }

// ============================================================
// Native Linear Sync
// ============================================================

// LinearSyncJobArgs carries the parameters for a native Linear sync job.
type LinearSyncJobArgs struct {
	ProjectID     uuid.UUID `json:"project_id"`
	IntegrationID uuid.UUID `json:"integration_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
}

func (LinearSyncJobArgs) Kind() string { return "linear_sync" }

// ============================================================
// Native Jira Sync
// ============================================================

// JiraSyncJobArgs carries the parameters for a native Jira sync job.
type JiraSyncJobArgs struct {
	ProjectID     uuid.UUID `json:"project_id"`
	IntegrationID uuid.UUID `json:"integration_id"`
	RunID         uuid.UUID `json:"run_id"`
	TaskID        uuid.UUID `json:"task_id"`
}

func (JiraSyncJobArgs) Kind() string { return "jira_sync" }

// ============================================================
// Agent Code Generation Pipeline (v2)
// ============================================================

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