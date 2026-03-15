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
	claudeProviderName        = "claude-code"
	claudeProviderDisplayName = "Claude Code"
	claudeDefaultMaxTurns     = 50
	claudeDefaultAllowedTools = "Read,Write,Edit,Bash,Grep"
)

// ClaudeCodeProvider implements AgentProvider for the Claude Code CLI.
type ClaudeCodeProvider struct{}

// Name returns the provider identifier.
func (p ClaudeCodeProvider) Name() string {
	return claudeProviderName
}

// DisplayName returns the UI-facing provider name.
func (p ClaudeCodeProvider) DisplayName() string {
	return claudeProviderDisplayName
}

// ValidateConfig checks that the required API key is present.
func (p ClaudeCodeProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	if value := strings.TrimSpace(cfg.ExtraConfig["ANTHROPIC_API_KEY"]); value != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("ANTHROPIC_API_KEY is required")
}

// InstallInstructions returns installation guidance for Claude Code.
func (p ClaudeCodeProvider) InstallInstructions() string {
	return "npm install -g @anthropic-ai/claude-code"
}

// DetectInstalled checks if the `claude` binary is available in PATH.
func (p ClaudeCodeProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("claude")
	return err == nil
}

// BuildCommand builds the requested headless Claude command execution.
func (p ClaudeCodeProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	instructions, err := os.ReadFile(req.PromptFile)
	if err != nil {
		return nil, fmt.Errorf("read prompt file: %w", err)
	}

	prompt := strings.TrimSpace(string(instructions))
	if prompt == "" {
		return nil, errors.New("prompt file is empty")
	}

	args := []string{
		"-p",
		prompt,
		"--output-format",
		"stream-json",
		"--allowedTools",
		claudeDefaultAllowedTools,
		"--dangerously-skip-permissions",
		"--max-turns",
		fmt.Sprintf("%d", claudeDefaultMaxTurns),
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	apiKey := strings.TrimSpace(req.Environment["ANTHROPIC_API_KEY"])
	if apiKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is required in execution environment")
	}
	env = append(env, "ANTHROPIC_API_KEY="+apiKey)
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses newline-delimited JSON output into ProgressEvent values.
func (p ClaudeCodeProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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
			if value, ok := payload["phase"].(string); ok && value != "" {
				event.Phase = value
			}
			if value, ok := payload["level"].(string); ok && value != "" {
				event.Level = value
			}
			if value, ok := payload["message"].(string); ok && value != "" {
				event.Message = value
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
