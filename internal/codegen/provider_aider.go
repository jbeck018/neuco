package codegen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	aiderProviderName        = "aider"
	aiderProviderDisplayName = "Aider"
)

// AiderProvider implements AgentProvider for the Aider CLI.
type AiderProvider struct{}

// Name returns the provider identifier.
func (p AiderProvider) Name() string {
	return aiderProviderName
}

// DisplayName returns the UI-facing provider name.
func (p AiderProvider) DisplayName() string {
	return aiderProviderDisplayName
}

// ValidateConfig checks that one of the required API keys is present.
func (p AiderProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	openAIKey := strings.TrimSpace(cfg.ExtraConfig["OPENAI_API_KEY"])
	anthropicKey := strings.TrimSpace(cfg.ExtraConfig["ANTHROPIC_API_KEY"])
	if openAIKey != "" || anthropicKey != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("OPENAI_API_KEY or ANTHROPIC_API_KEY is required")
}

// InstallInstructions returns installation guidance for Aider.
func (p AiderProvider) InstallInstructions() string {
	return "python -m pip install aider-install && aider-install"
}

// DetectInstalled checks if the `aider` binary is available in PATH.
func (p AiderProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("aider")
	return err == nil
}

// BuildCommand builds the requested non-interactive Aider command execution.
func (p AiderProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	instructions, err := os.ReadFile(req.PromptFile)
	if err != nil {
		return nil, fmt.Errorf("read prompt file: %w", err)
	}

	prompt := strings.TrimSpace(string(instructions))
	if prompt == "" {
		return nil, errors.New("prompt file is empty")
	}

	args := []string{
		"--yes",
		"--message",
		prompt,
		"--no-pretty",
		"--analytics-disable",
	}

	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command("aider", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	openAIKey := strings.TrimSpace(req.Environment["OPENAI_API_KEY"])
	anthropicKey := strings.TrimSpace(req.Environment["ANTHROPIC_API_KEY"])
	if openAIKey == "" && anthropicKey == "" {
		return nil, errors.New("OPENAI_API_KEY or ANTHROPIC_API_KEY is required in execution environment")
	}
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses plain-text Aider output into ProgressEvent values.
func (p AiderProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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
				Message:   line,
				Raw:       line,
				Timestamp: time.Now().UTC(),
			}

			if strings.HasPrefix(line, ">") || strings.Contains(line, "EDIT") || strings.Contains(line, "Applied edit") {
				event.Phase = "coding"
			}
			if strings.Contains(line, "Error") || strings.Contains(line, "error") {
				event.Level = "error"
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
