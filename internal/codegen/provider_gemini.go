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
	geminiProviderName        = "gemini"
	geminiProviderDisplayName = "Gemini CLI"
)

// GeminiProvider implements AgentProvider for the Gemini CLI.
type GeminiProvider struct{}

// Name returns the provider identifier.
func (p GeminiProvider) Name() string {
	return geminiProviderName
}

// DisplayName returns the UI-facing provider name.
func (p GeminiProvider) DisplayName() string {
	return geminiProviderDisplayName
}

// ValidateConfig checks that the required API key is present.
func (p GeminiProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	if value := strings.TrimSpace(cfg.ExtraConfig["GEMINI_API_KEY"]); value != "" {
		return nil
	}
	if value := strings.TrimSpace(cfg.ExtraConfig["GOOGLE_API_KEY"]); value != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("GEMINI_API_KEY or GOOGLE_API_KEY is required")
}

// InstallInstructions returns installation guidance for Gemini CLI.
func (p GeminiProvider) InstallInstructions() string {
	return "npm install -g @google/gemini-cli"
}

// DetectInstalled checks if the `gemini` binary is available in PATH.
func (p GeminiProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("gemini")
	return err == nil
}

// BuildCommand builds the requested headless Gemini CLI command execution.
func (p GeminiProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
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
		"--yolo",
		"--format",
		"json",
	}

	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command("gemini", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	geminiAPIKey := strings.TrimSpace(req.Environment["GEMINI_API_KEY"])
	googleAPIKey := strings.TrimSpace(req.Environment["GOOGLE_API_KEY"])
	if geminiAPIKey == "" && googleAPIKey == "" {
		return nil, errors.New("GEMINI_API_KEY or GOOGLE_API_KEY is required in execution environment")
	}
	if geminiAPIKey != "" {
		env = append(env, "GEMINI_API_KEY="+geminiAPIKey)
	}
	if googleAPIKey != "" {
		env = append(env, "GOOGLE_API_KEY="+googleAPIKey)
	}
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses newline-delimited JSON output into ProgressEvent values.
func (p GeminiProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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
			if value, ok := payload["content"].(string); ok && value != "" && event.Message == "agent event" {
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
