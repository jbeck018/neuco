package codegen

import (
	"context"
	"errors"
	"fmt"
	"github.com/neuco-ai/neuco/internal/config"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SandboxManager abstracts sandbox lifecycle and command execution.
type SandboxManager interface {
	// Provision creates a new sandbox environment.
	Provision(ctx context.Context, cfg SandboxConfig) (*Sandbox, error)

	// WriteFiles writes files into the sandbox filesystem.
	WriteFiles(ctx context.Context, sb *Sandbox, files map[string]string) error

	// Execute runs a command inside the sandbox and returns the result.
	Execute(ctx context.Context, sb *Sandbox, cmd string, args ...string) (*ExecResult, error)

	// StreamOutput starts a command and returns a channel of output lines.
	StreamOutput(ctx context.Context, sb *Sandbox, cmd string, args ...string) (<-chan LogEntry, error)

	// CollectDiff runs git diff inside the sandbox and returns file changes.
	CollectDiff(ctx context.Context, sb *Sandbox) ([]FileChange, error)

	// Destroy tears down the sandbox and cleans up resources.
	Destroy(ctx context.Context, sandboxID string) error
}

// SandboxConfig controls sandbox provisioning and execution behavior.
type SandboxConfig struct {
	GenerationID    string            `json:"generation_id"`
	RepoURL         string            `json:"repo_url"`
	RepoRef         string            `json:"repo_ref"`
	InstallCommands []string          `json:"install_commands,omitempty"`
	AgentBinaries   []string          `json:"agent_binaries,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	Environment     map[string]string `json:"environment,omitempty"`
	WorkingDir      string            `json:"working_dir,omitempty"`
}

// Sandbox represents an isolated execution environment.
type Sandbox struct {
	ID        string            `json:"id"`
	Provider  string            `json:"provider"` // e2b, docker, local
	WorkDir   string            `json:"work_dir"`
	Status    string            `json:"status"` // provisioning, ready, running, completed, failed, destroyed
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ExecResult stores process execution output and status.
type ExecResult struct {
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
}

// LogEntry is a single streamed output line from sandbox execution.
type LogEntry struct {
	Source    string    `json:"source"` // stdout, stderr, system
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// NewSandboxManager returns a sandbox manager implementation based on provider.
// Currently only the local provider is implemented.
func NewSandboxManager(provider string, cfg *config.Config) (SandboxManager, error) {
	selected := strings.ToLower(strings.TrimSpace(provider))
	if selected == "" && cfg != nil {
		selected = strings.ToLower(strings.TrimSpace(cfg.SandboxProvider))
	}
	if selected == "" {
		selected = "local"
	}

	switch selected {
	case "local":
		basePath := strings.TrimSpace(os.Getenv("NEUCO_LOCAL_SANDBOX_BASE_PATH"))
		if basePath == "" {
			basePath = filepath.Join(os.TempDir(), "neuco-sandboxes")
		}
		return NewLocalSandboxManager(basePath), nil
	case "e2b":
		if cfg == nil {
			return nil, errors.New("sandbox provider \"e2b\" requires config")
		}
		return NewE2BSandboxManager(cfg.E2BAPIKey, cfg.SandboxE2BTemplate), nil
	case "docker":
		return NewDockerSandboxManager(""), nil
	default:
		return nil, fmt.Errorf("unknown sandbox provider %q", selected)
	}
}
