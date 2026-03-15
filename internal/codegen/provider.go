package codegen

import (
	"context"
	"io"
	"os/exec"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
)

// AgentProvider defines the contract for a CLI coding agent integration.
type AgentProvider interface {
	// Name returns the provider identifier (e.g. "claude-code").
	Name() string

	// DisplayName returns a human-friendly provider name.
	DisplayName() string

	// ValidateConfig validates provider-specific configuration.
	ValidateConfig(ctx context.Context, cfg AgentConfig) error

	// InstallInstructions returns human-readable install steps.
	InstallInstructions() string

	// DetectInstalled checks whether the provider binary is available.
	DetectInstalled(pathEnv string) bool

	// BuildCommand builds a headless command execution for the provider.
	BuildCommand(req ExecutionRequest) (*exec.Cmd, error)

	// ParseOutput parses provider stdout into structured progress events.
	ParseOutput(r io.Reader) <-chan ProgressEvent
}

// AgentConfig stores project/org provider configuration and encrypted credentials.
type AgentConfig struct {
	ID              uuid.UUID         `json:"id"`
	OrgID           uuid.UUID         `json:"org_id"`
	ProjectID       *uuid.UUID        `json:"project_id,omitempty"`
	Provider        string            `json:"provider"`
	EncryptedAPIKey []byte            `json:"encrypted_api_key,omitempty"`
	ModelOverride   string            `json:"model_override,omitempty"`
	ExtraConfig     map[string]string `json:"extra_config,omitempty"`
	IsDefault       bool              `json:"is_default"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ExecutionRequest captures all required inputs for a provider execution.
type ExecutionRequest struct {
	GenerationID uuid.UUID         `json:"generation_id"`
	OrgID        uuid.UUID         `json:"org_id"`
	ProjectID    uuid.UUID         `json:"project_id"`
	Provider     string            `json:"provider"`
	SandboxPath  string            `json:"sandbox_path"`
	PromptFile   string            `json:"prompt_file"`
	Spec         domain.Spec       `json:"spec"`
	Environment  map[string]string `json:"environment,omitempty"`
	Timeout      time.Duration     `json:"timeout"`
	Model        string            `json:"model,omitempty"`
	MaxTurns     int               `json:"max_turns,omitempty"`
	AllowedTools []string          `json:"allowed_tools,omitempty"`
}

// ExecutionResult is the final outcome of a provider run.
type ExecutionResult struct {
	GenerationID uuid.UUID      `json:"generation_id"`
	Provider     string         `json:"provider"`
	Success      bool           `json:"success"`
	ExitCode     int            `json:"exit_code"`
	Duration     time.Duration  `json:"duration"`
	FileChanges  []FileChange   `json:"file_changes,omitempty"`
	AgentLog     []ProgressEvent `json:"agent_log,omitempty"`
	Error        string         `json:"error,omitempty"`
	StartedAt    time.Time      `json:"started_at"`
	CompletedAt  time.Time      `json:"completed_at"`
}

// FileChange describes a single repository file mutation produced by the agent.
type FileChange struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
	Diff       string `json:"diff,omitempty"`
	Content    string `json:"content,omitempty"`
}

// ProgressEvent is a normalized event emitted while the provider executes.
type ProgressEvent struct {
	GenerationID uuid.UUID         `json:"generation_id"`
	Provider     string            `json:"provider,omitempty"`
	Phase        string            `json:"phase"`
	Level        string            `json:"level"`
	Message      string            `json:"message"`
	Raw          string            `json:"raw,omitempty"`
	Timestamp    time.Time         `json:"timestamp"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ProviderRegistry stores and resolves providers by name.
type ProviderRegistry struct {
	providers map[string]AgentProvider
}

// NewProviderRegistry creates a provider registry with optional initial providers.
func NewProviderRegistry(list ...AgentProvider) *ProviderRegistry {
	registry := &ProviderRegistry{
		providers: make(map[string]AgentProvider, len(list)),
	}

	for _, provider := range list {
		if provider == nil {
			continue
		}
		registry.providers[provider.Name()] = provider
	}

	return registry
}

// Register registers (or overwrites) a provider by its Name().
func (r *ProviderRegistry) Register(provider AgentProvider) {
	if provider == nil {
		return
	}

	if r.providers == nil {
		r.providers = make(map[string]AgentProvider)
	}

	r.providers[provider.Name()] = provider
}

// Get resolves a provider by name.
func (r *ProviderRegistry) Get(name string) (AgentProvider, bool) {
	provider, ok := r.providers[name]
	return provider, ok
}

// List returns all registered provider names in sorted order.
func (r *ProviderRegistry) List() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
