package codegen

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultDockerImage       = "neuco-codegen:latest"
	defaultDockerCPULimit    = "2"
	defaultDockerMemoryLimit = "4g"
)

type dockerContainerState struct {
	containerID string
	config      SandboxConfig
}

// DockerSandboxManager manages sandbox execution via Docker CLI.
type DockerSandboxManager struct {
	imageName   string
	cpuLimit    string
	memoryLimit string

	mu         sync.RWMutex
	containers map[string]*dockerContainerState
}

// NewDockerSandboxManager constructs a Docker-backed sandbox manager.
func NewDockerSandboxManager(imageName string) *DockerSandboxManager {
	resolvedImage := strings.TrimSpace(imageName)
	if resolvedImage == "" {
		resolvedImage = defaultDockerImage
	}

	return &DockerSandboxManager{
		imageName:   resolvedImage,
		cpuLimit:    defaultDockerCPULimit,
		memoryLimit: defaultDockerMemoryLimit,
		containers:  make(map[string]*dockerContainerState),
	}
}

// Provision creates a Docker container sandbox and clones the configured repository.
func (m *DockerSandboxManager) Provision(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
	if strings.TrimSpace(cfg.RepoURL) == "" {
		return nil, errors.New("sandbox provision: repo URL is required")
	}

	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultSandboxTimeoutSeconds
	}

	containerName := "sandbox-" + uuid.NewString()
	createArgs := []string{
		"create",
		"--cpus=" + m.cpuLimit,
		"--memory=" + m.memoryLimit,
		"--pids-limit=512",
		"--name", containerName,
		"-w", "/workspace/repo",
		m.imageName,
		"sleep", "infinity",
	}

	createOut, createErrOut, createExit, err := m.runDockerCommand(ctx, nil, createArgs...)
	if err != nil {
		return nil, fmt.Errorf("sandbox provision: docker create: %w", err)
	}
	if createExit != 0 {
		return nil, fmt.Errorf("sandbox provision: docker create failed (exit %d): %s", createExit, strings.TrimSpace(createErrOut))
	}

	containerID := strings.TrimSpace(createOut)
	if containerID == "" {
		return nil, errors.New("sandbox provision: docker create did not return container ID")
	}

	cleanupOnError := func() {
		_ = m.Destroy(context.Background(), containerID)
	}

	if _, stderr, exitCode, execErr := m.runDockerCommand(ctx, nil, "start", containerID); execErr != nil {
		cleanupOnError()
		return nil, fmt.Errorf("sandbox provision: docker start: %w", execErr)
	} else if exitCode != 0 {
		cleanupOnError()
		return nil, fmt.Errorf("sandbox provision: docker start failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}

	if err := m.cloneRepo(ctx, containerID, cfg.RepoURL, cfg.RepoRef); err != nil {
		cleanupOnError()
		return nil, err
	}

	if _, stderr, exitCode, execErr := m.execInContainer(ctx, containerID, "", "mkdir -p /workspace/repo/.neuco", nil); execErr != nil {
		cleanupOnError()
		return nil, fmt.Errorf("sandbox provision: create .neuco directory: %w", execErr)
	} else if exitCode != 0 {
		cleanupOnError()
		return nil, fmt.Errorf("sandbox provision: create .neuco directory failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}

	workDir := "/workspace/repo"
	if override := strings.TrimSpace(cfg.WorkingDir); override != "" {
		resolved, resolveErr := resolveWorkingDir(workDir, override)
		if resolveErr != nil {
			cleanupOnError()
			return nil, fmt.Errorf("sandbox provision: resolve working directory %q: %w", override, resolveErr)
		}

		if _, stderr, exitCode, execErr := m.execInContainer(ctx, containerID, "", fmt.Sprintf("mkdir -p %s", shellEscape(resolved)), nil); execErr != nil {
			cleanupOnError()
			return nil, fmt.Errorf("sandbox provision: create working directory %q: %w", resolved, execErr)
		} else if exitCode != 0 {
			cleanupOnError()
			return nil, fmt.Errorf("sandbox provision: create working directory %q failed (exit %d): %s", resolved, exitCode, strings.TrimSpace(stderr))
		}

		workDir = resolved
	}

	now := time.Now().UTC()
	sb := &Sandbox{
		ID:        containerID,
		Provider:  "docker",
		WorkDir:   workDir,
		Status:    "ready",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(timeoutSeconds) * time.Second),
		Metadata: map[string]string{
			"container_name": containerName,
			"repo_ref":       strings.TrimSpace(cfg.RepoRef),
		},
	}

	m.mu.Lock()
	m.containers[containerID] = &dockerContainerState{containerID: containerID, config: cfg}
	m.mu.Unlock()

	return sb, nil
}

// WriteFiles writes the provided files into the sandbox working directory.
func (m *DockerSandboxManager) WriteFiles(ctx context.Context, sb *Sandbox, files map[string]string) error {
	if sb == nil {
		return errors.New("sandbox write files: sandbox is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sandbox write files: context cancelled: %w", err)
	}

	for relPath, content := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sandbox write files: context cancelled: %w", err)
		}

		absPath, err := resolveSandboxFilePath(sb.WorkDir, relPath)
		if err != nil {
			return fmt.Errorf("sandbox write files: %w", err)
		}

		dir := path.Dir(absPath)
		command := fmt.Sprintf("mkdir -p %s && cat > %s", shellEscape(dir), shellEscape(absPath))
		_, stderr, exitCode, execErr := m.execInContainer(ctx, sb.ID, "", command, strings.NewReader(content))
		if execErr != nil {
			return fmt.Errorf("sandbox write files: write %q: %w", relPath, execErr)
		}
		if exitCode != 0 {
			return fmt.Errorf("sandbox write files: write %q failed (exit %d): %s", relPath, exitCode, strings.TrimSpace(stderr))
		}
	}

	return nil
}

// Execute runs a command in the sandbox and captures output.
func (m *DockerSandboxManager) Execute(ctx context.Context, sb *Sandbox, cmd string, args ...string) (*ExecResult, error) {
	if sb == nil {
		return nil, errors.New("sandbox execute: sandbox is required")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("sandbox execute: command is required")
	}

	execCtx, cancel := m.withSandboxTimeout(ctx, sb)
	defer cancel()

	command := buildCommandString(cmd, args...)
	started := time.Now()
	stdout, stderr, exitCode, err := m.execInContainer(execCtx, sb.ID, sb.WorkDir, command, nil)
	duration := time.Since(started)
	if err != nil {
		return nil, fmt.Errorf("sandbox execute: run %q: %w", command, err)
	}

	return &ExecResult{
		Command:  command,
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
		Duration: duration,
	}, nil
}

// StreamOutput runs a command and streams output lines as LogEntry values.
func (m *DockerSandboxManager) StreamOutput(ctx context.Context, sb *Sandbox, cmd string, args ...string) (<-chan LogEntry, error) {
	if sb == nil {
		return nil, errors.New("sandbox stream output: sandbox is required")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("sandbox stream output: command is required")
	}

	execCtx, cancel := m.withSandboxTimeout(ctx, sb)
	command := buildCommandString(cmd, args...)
	dockerArgs := []string{"exec", "-w", sb.WorkDir, sb.ID, "sh", "-c", command}
	dockerCmd := exec.CommandContext(execCtx, "docker", dockerArgs...)

	stdoutPipe, err := dockerCmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sandbox stream output: stdout pipe: %w", err)
	}
	stderrPipe, err := dockerCmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sandbox stream output: stderr pipe: %w", err)
	}

	if err := dockerCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("sandbox stream output: start command: %w", err)
	}

	out := make(chan LogEntry, 128)
	go func() {
		defer close(out)
		defer cancel()

		var wg sync.WaitGroup
		wg.Add(2)

		go m.streamReader(execCtx, &wg, out, "stdout", stdoutPipe)
		go m.streamReader(execCtx, &wg, out, "stderr", stderrPipe)

		wg.Wait()

		err := dockerCmd.Wait()
		now := time.Now().UTC()
		if err == nil {
			m.sendLog(execCtx, out, LogEntry{Source: "system", Message: "command completed successfully", Timestamp: now})
			return
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			m.sendLog(execCtx, out, LogEntry{Source: "system", Message: fmt.Sprintf("command exited with code %d", exitErr.ExitCode()), Timestamp: now})
			return
		}

		if execCtx.Err() != nil {
			m.sendLog(context.Background(), out, LogEntry{Source: "system", Message: fmt.Sprintf("command interrupted: %v", execCtx.Err()), Timestamp: now})
			return
		}

		m.sendLog(execCtx, out, LogEntry{Source: "system", Message: fmt.Sprintf("command failed: %v", err), Timestamp: now})
	}()

	return out, nil
}

// CollectDiff stages all changes, then collects per-file metadata and patch content.
func (m *DockerSandboxManager) CollectDiff(ctx context.Context, sb *Sandbox) ([]FileChange, error) {
	if sb == nil {
		return nil, errors.New("sandbox collect diff: sandbox is required")
	}

	if _, stderr, exitCode, err := m.execInContainer(ctx, sb.ID, sb.WorkDir, "git add -A", nil); err != nil {
		return nil, fmt.Errorf("sandbox collect diff: stage changes: %w", err)
	} else if exitCode != 0 {
		return nil, fmt.Errorf("sandbox collect diff: stage changes failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}

	nameStatusRaw, stderr, exitCode, err := m.execInContainer(ctx, sb.ID, sb.WorkDir, "git diff --cached --name-status", nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox collect diff: list changed files: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("sandbox collect diff: list changed files failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}

	lines := strings.Split(strings.TrimSpace(nameStatusRaw), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}

	changes := make([]FileChange, 0, len(lines))
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("sandbox collect diff: context cancelled: %w", err)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		change, err := parseNameStatusLine(line)
		if err != nil {
			return nil, fmt.Errorf("sandbox collect diff: parse line %q: %w", line, err)
		}

		diffCommand := fmt.Sprintf("git diff --cached -- %s", shellEscape(change.Path))
		diffRaw, diffErrRaw, diffExit, diffErr := m.execInContainer(ctx, sb.ID, sb.WorkDir, diffCommand, nil)
		if diffErr != nil {
			return nil, fmt.Errorf("sandbox collect diff: diff for %q: %w", change.Path, diffErr)
		}
		if diffExit != 0 {
			return nil, fmt.Errorf("sandbox collect diff: diff for %q failed (exit %d): %s", change.Path, diffExit, strings.TrimSpace(diffErrRaw))
		}
		change.Diff = diffRaw

		if change.ChangeType != "deleted" {
			fullPath, pathErr := resolveSandboxFilePath(sb.WorkDir, change.Path)
			if pathErr != nil {
				return nil, fmt.Errorf("sandbox collect diff: resolve path %q: %w", change.Path, pathErr)
			}
			catCommand := fmt.Sprintf("cat -- %s", shellEscape(fullPath))
			content, _, contentExit, contentErr := m.execInContainer(ctx, sb.ID, sb.WorkDir, catCommand, nil)
			if contentErr == nil && contentExit == 0 {
				change.Content = content
			}
		}

		changes = append(changes, change)
	}

	return changes, nil
}

// Destroy stops and removes a sandbox container.
func (m *DockerSandboxManager) Destroy(ctx context.Context, sandboxID string) error {
	trimmed := strings.TrimSpace(sandboxID)
	if trimmed == "" {
		return errors.New("sandbox destroy: sandbox ID is required")
	}

	_, stopStderr, stopExit, stopErr := m.runDockerCommand(ctx, nil, "stop", trimmed)
	if stopErr != nil {
		return fmt.Errorf("sandbox destroy: docker stop %q: %w", trimmed, stopErr)
	}

	_, rmStderr, rmExit, rmErr := m.runDockerCommand(ctx, nil, "rm", "-f", trimmed)
	if rmErr != nil {
		return fmt.Errorf("sandbox destroy: docker rm -f %q: %w", trimmed, rmErr)
	}

	m.mu.Lock()
	delete(m.containers, trimmed)
	m.mu.Unlock()

	if rmExit != 0 {
		if stopExit != 0 {
			return fmt.Errorf("sandbox destroy: docker stop failed (exit %d): %s; docker rm -f failed (exit %d): %s", stopExit, strings.TrimSpace(stopStderr), rmExit, strings.TrimSpace(rmStderr))
		}
		return fmt.Errorf("sandbox destroy: docker rm -f failed (exit %d): %s", rmExit, strings.TrimSpace(rmStderr))
	}

	if stopExit != 0 {
		return fmt.Errorf("sandbox destroy: docker stop failed (exit %d): %s", stopExit, strings.TrimSpace(stopStderr))
	}

	return nil
}

func (m *DockerSandboxManager) cloneRepo(ctx context.Context, containerID, repoURL, repoRef string) error {
	ref := strings.TrimSpace(repoRef)
	if ref != "" {
		cloneCmd := fmt.Sprintf("git clone --depth=1 --branch %s %s /workspace/repo", shellEscape(ref), shellEscape(repoURL))
		if _, stderr, exitCode, err := m.execInContainer(ctx, containerID, "", cloneCmd, nil); err == nil && exitCode == 0 {
			return nil
		} else {
			fallback := fmt.Sprintf("git clone --depth=1 %s /workspace/repo", shellEscape(repoURL))
			_, fallbackErrOut, fallbackExit, fallbackErr := m.execInContainer(ctx, containerID, "", fallback, nil)
			if fallbackErr == nil && fallbackExit == 0 {
				return nil
			}
			if fallbackErr != nil {
				return fmt.Errorf("sandbox provision: git clone with ref %q failed: %w", ref, fallbackErr)
			}
			if exitCode != 0 {
				return fmt.Errorf("sandbox provision: git clone with ref %q failed (initial exit %d): %s; fallback exit %d: %s", ref, exitCode, strings.TrimSpace(stderr), fallbackExit, strings.TrimSpace(fallbackErrOut))
			}
			return fmt.Errorf("sandbox provision: git clone with ref %q failed: fallback exit %d: %s", ref, fallbackExit, strings.TrimSpace(fallbackErrOut))
		}
	}

	cloneCmd := fmt.Sprintf("git clone --depth=1 %s /workspace/repo", shellEscape(repoURL))
	_, stderr, exitCode, err := m.execInContainer(ctx, containerID, "", cloneCmd, nil)
	if err != nil {
		return fmt.Errorf("sandbox provision: git clone failed: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("sandbox provision: git clone failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}

	return nil
}

func (m *DockerSandboxManager) execInContainer(ctx context.Context, containerID, workDir, command string, stdin io.Reader) (string, string, int, error) {
	dockerArgs := []string{"exec"}
	if stdin != nil {
		dockerArgs = append(dockerArgs, "-i")
	}
	if strings.TrimSpace(workDir) != "" {
		dockerArgs = append(dockerArgs, "-w", workDir)
	}
	dockerArgs = append(dockerArgs, containerID, "sh", "-c", command)
	return m.runDockerCommand(ctx, stdin, dockerArgs...)
}

func (m *DockerSandboxManager) runDockerCommand(ctx context.Context, stdin io.Reader, args ...string) (string, string, int, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		command.Stdin = stdin
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			return stdout.String(), stderr.String(), -1, fmt.Errorf("run docker %s: %w", strings.Join(args, " "), ctx.Err())
		} else {
			return stdout.String(), stderr.String(), -1, fmt.Errorf("run docker %s: %w", strings.Join(args, " "), err)
		}
	}

	return stdout.String(), stderr.String(), exitCode, nil
}

func (m *DockerSandboxManager) withSandboxTimeout(ctx context.Context, sb *Sandbox) (context.Context, context.CancelFunc) {
	if sb == nil || sb.ExpiresAt.IsZero() {
		return context.WithCancel(ctx)
	}

	remaining := time.Until(sb.ExpiresAt)
	if remaining <= 0 {
		return context.WithTimeout(ctx, time.Second)
	}

	return context.WithTimeout(ctx, remaining)
}

func (m *DockerSandboxManager) streamReader(ctx context.Context, wg *sync.WaitGroup, out chan<- LogEntry, source string, r io.Reader) {
	defer wg.Done()

	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		entry := LogEntry{
			Source:    source,
			Message:   scanner.Text(),
			Timestamp: time.Now().UTC(),
		}
		if !m.sendLog(ctx, out, entry) {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		_ = m.sendLog(ctx, out, LogEntry{
			Source:    "system",
			Message:   fmt.Sprintf("%s stream read error: %v", source, err),
			Timestamp: time.Now().UTC(),
		})
	}
}

func (m *DockerSandboxManager) sendLog(ctx context.Context, out chan<- LogEntry, entry LogEntry) bool {
	select {
	case out <- entry:
		return true
	case <-ctx.Done():
		return false
	}
}
