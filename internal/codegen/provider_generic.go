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
	genericProviderName        = "generic"
	genericProviderDisplayName = "Custom Agent"
)

// GenericProvider implements AgentProvider for a custom agent CLI.
type GenericProvider struct{}

// Name returns the provider identifier.
func (p GenericProvider) Name() string {
	return genericProviderName
}

// DisplayName returns the UI-facing provider name.
func (p GenericProvider) DisplayName() string {
	return genericProviderDisplayName
}

// ValidateConfig checks required generic provider configuration.
func (p GenericProvider) ValidateConfig(_ context.Context, cfg AgentConfig) error {
	binaryPath := strings.TrimSpace(cfg.ExtraConfig["binary_path"])
	if binaryPath == "" {
		return errors.New("binary_path is required in extra_config")
	}

	args := strings.TrimSpace(cfg.ExtraConfig["args"])
	if args != "" && strings.ContainsAny(args, "|;&$`()") {
		return errors.New("args contain disallowed shell metacharacters")
	}

	return nil
}

// InstallInstructions returns installation guidance for a custom agent.
func (p GenericProvider) InstallInstructions() string {
	return "Install your custom agent CLI and provide the binary path in configuration"
}

// DetectInstalled returns true because the binary path is provided at runtime.
func (p GenericProvider) DetectInstalled(_ string) bool {
	return true
}

// BuildCommand builds the requested custom agent command execution.
func (p GenericProvider) BuildCommand(req ExecutionRequest) (*exec.Cmd, error) {
	binary := strings.TrimSpace(req.Environment["NEUCO_GENERIC_BINARY"])
	if binary == "" {
		return nil, errors.New("NEUCO_GENERIC_BINARY environment variable is required")
	}

	args := make([]string, 0)
	if value := strings.TrimSpace(req.Environment["NEUCO_GENERIC_ARGS"]); value != "" {
		args = append(args, strings.Fields(value)...)
	}
	args = append(args, req.PromptFile)

	cmd := exec.Command(binary, args...)
	cmd.Dir = req.SandboxPath

	env := os.Environ()
	for key, value := range req.Environment {
		env = append(env, key+"="+value)
	}
	cmd.Env = env

	return cmd, nil
}

// ParseOutput parses JSON/text output into ProgressEvent values.
func (p GenericProvider) ParseOutput(r io.Reader) <-chan ProgressEvent {
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

			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err == nil {
				event.Message = "agent event"
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
