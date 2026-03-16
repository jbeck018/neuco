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
	slateProviderName        = "slate-cli"
	slateProviderDisplayName = "Slate CLI"
	slateDefaultMaxSteps     = 40
)

// SlateProvider implements AgentProvider for the Slate CLI.
type SlateProvider struct{}

// Name returns the provider identifier.
func (p SlateProvider) Name() string {
	return slateProviderName
}

// DisplayName returns the UI-facing provider name.
func (p SlateProvider) DisplayName() string {
	return slateProviderDisplayName
}

// ValidateConfig checks that the required API key is present.
func (p SlateProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	if value := strings.TrimSpace(cfg.ExtraConfig["SLATE_API_KEY"]); value != "" {
		return nil
	}
	if len(cfg.EncryptedAPIKey) > 0 {
		return nil
	}
	return errors.New("SLATE_API_KEY is required")
}

// InstallInstructions returns installation guidance for Slate CLI.
func (p SlateProvider) InstallInstructions() string {
	return "Contact Random Labs for Slate CLI distribution"
}

// DetectInstalled checks if the `slate` binary is available in PATH.
func (p SlateProvider) DetectInstalled(pathEnv string) bool {
	if pathEnv != "" {
		originalPath := os.Getenv("PATH")
		defer func() {
			_ = os.Setenv("PATH", originalPath)
		}()
		_ = os.Setenv("PATH", pathEnv)
	}

	_, err := exec.LookPath("slate")
	return err == nil
}

// BuildCommand builds the requested headless Slate CLI command execution.
func (p SlateProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	args := []string{
		"run",
		"--task-file",
		req.PromptFile,
		"--json",
		"--max-steps",
		fmt.Sprintf("%d", slateDefaultMaxSteps),
		"--sandbox-mode",
		"external",
	}

	cmd := exec.Command("slate", args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}

	apiKey := strings.TrimSpace(req.Environment["SLATE_API_KEY"])
	if apiKey == "" {
		return nil, errors.New("SLATE_API_KEY is required in execution environment")
	}
	env = append(env, "SLATE_API_KEY="+apiKey)
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses newline-delimited JSON output into ProgressEvent values.
func (p SlateProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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
				switch value {
				case "thinking":
					event.Phase = "planning"
				case "action":
					event.Phase = "coding"
				case "observation":
					event.Phase = "validating"
				case "result":
					event.Phase = "completed"
				default:
					event.Phase = value
				}
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
