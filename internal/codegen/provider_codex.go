package codegen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	codexProviderName        = "codex"
	codexProviderDisplayName = "OpenAI Codex"
)

// CodexProvider implements AgentProvider for the OpenAI Codex CLI.
type CodexProvider struct{}

// Name returns the provider identifier.
func (p CodexProvider) Name() string {
	return codexProviderName
}

// DisplayName returns the UI-facing provider name.
func (p CodexProvider) DisplayName() string {
	return codexProviderDisplayName
}

// ValidateConfig checks that the required API key is present.
func (p CodexProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	if value := strings.TrimSpace(cfg.ExtraConfig["OPENAI_API_KEY"]); value != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("OPENAI_API_KEY is required")
}

// InstallInstructions returns installation guidance for Codex.
func (p CodexProvider) InstallInstructions() string {
	return "npm install -g @openai/codex"
}

// DetectInstalled checks if the `codex` binary is available in PATH.
func (p CodexProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("codex")
	return err == nil
}

// BuildCommand builds the requested headless Codex command execution.
func (p CodexProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	if _, err := os.ReadFile(req.PromptFile); err != nil {
		return nil, fmt.Errorf("read prompt file: %w", err)
	}

	args := []string{
		"exec",
		"--json",
		"--no-interactive",
		"--task-file",
		req.PromptFile,
		"--workdir",
		".",
	}

	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command("codex", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	apiKey := strings.TrimSpace(req.Environment["OPENAI_API_KEY"])
	if apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is required in execution environment")
	}
	env = append(env, "OPENAI_API_KEY="+apiKey)
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses newline-delimited JSON output into ProgressEvent values.
func (p CodexProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
	out := make(chan ProgressEvent)

	go func() {
		defer close(out)

		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			event := ProgressEvent{
				Provider:  p.Name(),
				Phase:     "running",
				Level:     "info",
				Message:   "agent event",
				Raw:       line,
				Timestamp: time.Now().UTC(),
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				event.Level = "warn"
				event.Message = "failed to parse JSON event"
				out <- event
				continue
			}

			if value, ok := payload["type"].(string); ok && value != "" {
				event.Phase = value
			}
			if value, ok := payload["status"].(string); ok && value != "" {
				event.Phase = value
			}
			if value, ok := payload["phase"].(string); ok && value != "" {
				event.Phase = value
			}
			if value, ok := payload["level"].(string); ok && value != "" {
				event.Level = value
			}
			if value, ok := payload["message"].(string); ok && value != "" {
				event.Message = value
			}
			if value, ok := payload["summary"].(string); ok && value != "" && event.Message == "agent event" {
				event.Message = value
			}

			out <- event
		}

		if err := scanner.Err(); err != nil {
			out <- ProgressEvent{
				Provider:  p.Name(),
				Phase:     "error",
				Level:     "error",
				Message:   fmt.Sprintf("output stream error: %v", err),
				Timestamp: time.Now().UTC(),
			}
		}
	}()

	return out
}
