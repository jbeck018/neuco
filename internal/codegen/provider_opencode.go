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
	opencodeProviderName        = "opencode"
	opencodeProviderDisplayName = "OpenCode"
)

// OpenCodeProvider implements AgentProvider for the OpenCode CLI.
type OpenCodeProvider struct{}

// Name returns the provider identifier.
func (p OpenCodeProvider) Name() string {
	return opencodeProviderName
}

// DisplayName returns the UI-facing provider name.
func (p OpenCodeProvider) DisplayName() string {
	return opencodeProviderDisplayName
}

// ValidateConfig checks that the required API key is present.
func (p OpenCodeProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	if value := strings.TrimSpace(cfg.ExtraConfig["OPENAI_API_KEY"]); value != "" {
		return nil
	}
	if value := strings.TrimSpace(cfg.ExtraConfig["ANTHROPIC_API_KEY"]); value != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("OPENAI_API_KEY or ANTHROPIC_API_KEY is required")
}

// InstallInstructions returns installation guidance for OpenCode.
func (p OpenCodeProvider) InstallInstructions() string {
	return "npm install -g opencode-ai"
}

// DetectInstalled checks if the `opencode` binary is available in PATH.
func (p OpenCodeProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("opencode")
	return err == nil
}

// BuildCommand builds the requested headless OpenCode command execution.
func (p OpenCodeProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	args := []string{
		"run",
		"--non-interactive",
		"--json",
		"--task",
		req.PromptFile,
	}

	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command("opencode", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	openAIAPIKey := strings.TrimSpace(req.Environment["OPENAI_API_KEY"])
	anthropicAPIKey := strings.TrimSpace(req.Environment["ANTHROPIC_API_KEY"])
	if openAIAPIKey == "" && anthropicAPIKey == "" {
		return nil, errors.New("OPENAI_API_KEY or ANTHROPIC_API_KEY is required in execution environment")
	}
	if openAIAPIKey != "" {
		env = append(env, "OPENAI_API_KEY="+openAIAPIKey)
	}
	if anthropicAPIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+anthropicAPIKey)
	}
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses newline-delimited JSON output into ProgressEvent values.
func (p OpenCodeProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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
				event.Phase = "error"
				event.Level = "error"
				event.Message = fmt.Sprintf("failed to parse JSON event: %v", err)
				out <- event
				continue
			}

			if value, ok := payload["type"].(string); ok && value != "" {
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
			if value, ok := payload["event"].(string); ok && value != "" && event.Phase == "running" {
				event.Phase = value
			}
			if value, ok := payload["text"].(string); ok && value != "" && event.Message == "agent event" {
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
