package codegen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultE2BBaseURL = "https://api.e2b.dev"

type e2bSandboxState struct {
	config SandboxConfig
}

// E2BSandboxManager manages sandbox execution via the E2B REST API.
type E2BSandboxManager struct {
	apiKey     string
	template   string
	httpClient *http.Client
	baseURL    string

	mu        sync.RWMutex
	sandboxes map[string]*e2bSandboxState
}

// NewE2BSandboxManager constructs an E2B-backed sandbox manager.
func NewE2BSandboxManager(apiKey, template string) *E2BSandboxManager {
	return &E2BSandboxManager{
		apiKey:     strings.TrimSpace(apiKey),
		template:   strings.TrimSpace(template),
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    defaultE2BBaseURL,
		sandboxes:  make(map[string]*e2bSandboxState),
	}
}

// Provision creates a remote E2B sandbox and clones the configured repository.
func (m *E2BSandboxManager) Provision(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
	if strings.TrimSpace(m.apiKey) == "" {
		return nil, errors.New("sandbox provision: E2B API key is required")
	}
	if strings.TrimSpace(cfg.RepoURL) == "" {
		return nil, errors.New("sandbox provision: repo URL is required")
	}

	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultSandboxTimeoutSeconds
	}

	createReq := map[string]any{
		"templateID": strings.TrimSpace(m.template),
		"timeout":    timeoutSeconds,
	}
	if strings.TrimSpace(m.template) == "" {
		delete(createReq, "templateID")
	}

	var createResp struct {
		ID string `json:"id"`
	}

	if err := m.doJSON(ctx, http.MethodPost, "/sandboxes", createReq, &createResp); err != nil {
		return nil, fmt.Errorf("sandbox provision: create E2B sandbox: %w", err)
	}
	if strings.TrimSpace(createResp.ID) == "" {
		return nil, errors.New("sandbox provision: E2B create response missing sandbox ID")
	}

	sandboxID := strings.TrimSpace(createResp.ID)
	repoDir := "/workspace/repo"

	if _, err := m.executeCommand(ctx, sandboxID, fmt.Sprintf("mkdir -p %s", shellEscape(repoDir)), ""); err != nil {
		_ = m.Destroy(context.Background(), sandboxID)
		return nil, fmt.Errorf("sandbox provision: create repo directory: %w", err)
	}

	if err := m.cloneRepo(ctx, sandboxID, cfg.RepoURL, cfg.RepoRef, repoDir); err != nil {
		_ = m.Destroy(context.Background(), sandboxID)
		return nil, err
	}

	if _, err := m.executeCommand(ctx, sandboxID, "mkdir -p .neuco", repoDir); err != nil {
		_ = m.Destroy(context.Background(), sandboxID)
		return nil, fmt.Errorf("sandbox provision: create .neuco directory: %w", err)
	}

	workDir := repoDir
	if override := strings.TrimSpace(cfg.WorkingDir); override != "" {
		resolved, err := resolveWorkingDir(repoDir, override)
		if err != nil {
			_ = m.Destroy(context.Background(), sandboxID)
			return nil, fmt.Errorf("sandbox provision: resolve working directory %q: %w", override, err)
		}

		if _, err := m.executeCommand(ctx, sandboxID, fmt.Sprintf("mkdir -p %s", shellEscape(resolved)), ""); err != nil {
			_ = m.Destroy(context.Background(), sandboxID)
			return nil, fmt.Errorf("sandbox provision: create working directory %q: %w", resolved, err)
		}
		workDir = resolved
	}

	now := time.Now().UTC()
	sb := &Sandbox{
		ID:        sandboxID,
		Provider:  "e2b",
		WorkDir:   workDir,
		Status:    "ready",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(timeoutSeconds) * time.Second),
		Metadata: map[string]string{
			"repo_ref": strings.TrimSpace(cfg.RepoRef),
		},
	}

	m.mu.Lock()
	m.sandboxes[sandboxID] = &e2bSandboxState{config: cfg}
	m.mu.Unlock()

	slog.Debug("sandbox provisioned", "provider", "e2b", "sandbox_id", sandboxID, "work_dir", workDir)
	return sb, nil
}

// WriteFiles writes the provided files into the sandbox working directory.
func (m *E2BSandboxManager) WriteFiles(ctx context.Context, sb *Sandbox, files map[string]string) error {
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

		if err := m.writeFile(ctx, sb.ID, absPath, content); err != nil {
			return fmt.Errorf("sandbox write files: write %q: %w", relPath, err)
		}
	}

	return nil
}

// Execute runs a command in the sandbox and captures output.
func (m *E2BSandboxManager) Execute(ctx context.Context, sb *Sandbox, cmd string, args ...string) (*ExecResult, error) {
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
	rsp, err := m.executeCommand(execCtx, sb.ID, command, sb.WorkDir)
	duration := time.Since(started)
	if err != nil {
		return nil, fmt.Errorf("sandbox execute: run %q: %w", command, err)
	}

	result := &ExecResult{
		Command:  command,
		ExitCode: rsp.ExitCode,
		Stdout:   rsp.Stdout,
		Stderr:   rsp.Stderr,
		Duration: duration,
	}

	return result, nil
}

// StreamOutput runs a command and streams output lines as LogEntry values.
func (m *E2BSandboxManager) StreamOutput(ctx context.Context, sb *Sandbox, cmd string, args ...string) (<-chan LogEntry, error) {
	if sb == nil {
		return nil, errors.New("sandbox stream output: sandbox is required")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("sandbox stream output: command is required")
	}

	execCtx, cancel := m.withSandboxTimeout(ctx, sb)
	out := make(chan LogEntry, 128)
	command := buildCommandString(cmd, args...)

	go func() {
		defer close(out)
		defer cancel()

		if err := m.streamCommand(execCtx, sb.ID, command, sb.WorkDir, out); err != nil {
			m.sendLog(execCtx, out, LogEntry{
				Source:    "system",
				Message:   fmt.Sprintf("command failed: %v", err),
				Timestamp: time.Now().UTC(),
			})
		}
	}()

	return out, nil
}

// CollectDiff stages all changes, then collects per-file metadata and patch content.
func (m *E2BSandboxManager) CollectDiff(ctx context.Context, sb *Sandbox) ([]FileChange, error) {
	if sb == nil {
		return nil, errors.New("sandbox collect diff: sandbox is required")
	}

	if _, err := m.executeCommand(ctx, sb.ID, "git add -A", sb.WorkDir); err != nil {
		return nil, fmt.Errorf("sandbox collect diff: stage changes: %w", err)
	}

	nameStatusRsp, err := m.executeCommand(ctx, sb.ID, "git diff --cached --name-status", sb.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox collect diff: list changed files: %w", err)
	}
	if nameStatusRsp.ExitCode != 0 {
		return nil, fmt.Errorf("sandbox collect diff: list changed files: exit code %d: %s", nameStatusRsp.ExitCode, strings.TrimSpace(nameStatusRsp.Stderr))
	}

	lines := strings.Split(strings.TrimSpace(nameStatusRsp.Stdout), "\n")
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

		diffCmd := fmt.Sprintf("git diff --cached -- %s", shellEscape(change.Path))
		diffRsp, err := m.executeCommand(ctx, sb.ID, diffCmd, sb.WorkDir)
		if err != nil {
			return nil, fmt.Errorf("sandbox collect diff: diff for %q: %w", change.Path, err)
		}
		if diffRsp.ExitCode != 0 {
			return nil, fmt.Errorf("sandbox collect diff: diff for %q exit code %d: %s", change.Path, diffRsp.ExitCode, strings.TrimSpace(diffRsp.Stderr))
		}
		change.Diff = diffRsp.Stdout

		if change.ChangeType != "deleted" {
			fullPath, err := resolveSandboxFilePath(sb.WorkDir, change.Path)
			if err != nil {
				return nil, fmt.Errorf("sandbox collect diff: resolve path %q: %w", change.Path, err)
			}
			content, readErr := m.readFile(ctx, sb.ID, fullPath)
			if readErr == nil {
				change.Content = content
			}
		}

		changes = append(changes, change)
	}

	return changes, nil
}

// Destroy tears down a sandbox and cleans up manager state.
func (m *E2BSandboxManager) Destroy(ctx context.Context, sandboxID string) error {
	trimmed := strings.TrimSpace(sandboxID)
	if trimmed == "" {
		return errors.New("sandbox destroy: sandbox ID is required")
	}

	if err := m.doJSON(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(trimmed), nil, nil); err != nil {
		return fmt.Errorf("sandbox destroy: delete sandbox %q: %w", trimmed, err)
	}

	m.mu.Lock()
	delete(m.sandboxes, trimmed)
	m.mu.Unlock()

	slog.Debug("sandbox destroyed", "provider", "e2b", "sandbox_id", trimmed)
	return nil
}

type e2bCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func (m *E2BSandboxManager) cloneRepo(ctx context.Context, sandboxID, repoURL, repoRef, repoDir string) error {
	ref := strings.TrimSpace(repoRef)
	if ref != "" {
		cloneCmd := fmt.Sprintf("git clone --depth=1 --branch %s %s %s", shellEscape(ref), shellEscape(repoURL), shellEscape(repoDir))
		if rsp, err := m.executeCommand(ctx, sandboxID, cloneCmd, ""); err == nil && rsp.ExitCode == 0 {
			return nil
		}

		fallback := fmt.Sprintf("git clone --depth=1 %s %s", shellEscape(repoURL), shellEscape(repoDir))
		rsp, fallbackErr := m.executeCommand(ctx, sandboxID, fallback, "")
		if fallbackErr == nil && rsp.ExitCode == 0 {
			return nil
		}

		if fallbackErr != nil {
			return fmt.Errorf("sandbox provision: git clone with ref %q failed: %w", ref, fallbackErr)
		}
		return fmt.Errorf("sandbox provision: git clone with ref %q failed: exit code %d: %s", ref, rsp.ExitCode, strings.TrimSpace(rsp.Stderr))
	}

	cloneCmd := fmt.Sprintf("git clone --depth=1 %s %s", shellEscape(repoURL), shellEscape(repoDir))
	rsp, err := m.executeCommand(ctx, sandboxID, cloneCmd, "")
	if err != nil {
		return fmt.Errorf("sandbox provision: git clone failed: %w", err)
	}
	if rsp.ExitCode != 0 {
		return fmt.Errorf("sandbox provision: git clone failed: exit code %d: %s", rsp.ExitCode, strings.TrimSpace(rsp.Stderr))
	}

	return nil
}

func (m *E2BSandboxManager) executeCommand(ctx context.Context, sandboxID, command, workDir string) (*e2bCommandResult, error) {
	payload := map[string]any{
		"command": command,
	}
	if strings.TrimSpace(workDir) != "" {
		payload["workDir"] = workDir
	}

	var raw map[string]any
	path := "/sandboxes/" + url.PathEscape(strings.TrimSpace(sandboxID)) + "/commands"
	if err := m.doJSON(ctx, http.MethodPost, path, payload, &raw); err != nil {
		return nil, err
	}

	result := &e2bCommandResult{
		Stdout:   getStringAny(raw, "stdout", "output.stdout", "data.stdout", "result.stdout"),
		Stderr:   getStringAny(raw, "stderr", "output.stderr", "data.stderr", "result.stderr"),
		ExitCode: getIntAny(raw, "exitCode", "exit_code", "data.exitCode", "result.exitCode"),
	}

	if result.Stdout == "" && result.Stderr == "" {
		if out := getStringAny(raw, "output", "message", "data.output"); out != "" {
			result.Stdout = out
		}
	}

	return result, nil
}

func (m *E2BSandboxManager) streamCommand(ctx context.Context, sandboxID, command, workDir string, out chan<- LogEntry) error {
	payload := map[string]any{
		"command": command,
		"stream":  true,
	}
	if strings.TrimSpace(workDir) != "" {
		payload["workDir"] = workDir
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal streaming command payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/sandboxes/"+url.PathEscape(strings.TrimSpace(sandboxID))+"/commands", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build streaming command request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute streaming command request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("stream command: failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("streaming command failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}

	type streamEvent struct {
		Source   string `json:"source"`
		Type     string `json:"type"`
		Message  string `json:"message"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		Done     bool   `json:"done"`
		ExitCode *int   `json:"exitCode"`
	}

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" {
			continue
		}
		if line == "[DONE]" {
			m.sendLog(ctx, out, LogEntry{Source: "system", Message: "command completed successfully", Timestamp: time.Now().UTC()})
			continue
		}

		var evt streamEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			m.sendLog(ctx, out, LogEntry{Source: "stdout", Message: line, Timestamp: time.Now().UTC()})
			continue
		}

		now := time.Now().UTC()
		if evt.Stdout != "" {
			for _, msg := range strings.Split(strings.ReplaceAll(evt.Stdout, "\r\n", "\n"), "\n") {
				if strings.TrimSpace(msg) == "" {
					continue
				}
				if !m.sendLog(ctx, out, LogEntry{Source: "stdout", Message: msg, Timestamp: now}) {
					return nil
				}
			}
		}
		if evt.Stderr != "" {
			for _, msg := range strings.Split(strings.ReplaceAll(evt.Stderr, "\r\n", "\n"), "\n") {
				if strings.TrimSpace(msg) == "" {
					continue
				}
				if !m.sendLog(ctx, out, LogEntry{Source: "stderr", Message: msg, Timestamp: now}) {
					return nil
				}
			}
		}
		if evt.Message != "" {
			source := strings.TrimSpace(evt.Source)
			if source == "" {
				source = "system"
			}
			if !m.sendLog(ctx, out, LogEntry{Source: source, Message: evt.Message, Timestamp: now}) {
				return nil
			}
		}
		if evt.Done {
			if evt.ExitCode != nil {
				m.sendLog(ctx, out, LogEntry{Source: "system", Message: fmt.Sprintf("command exited with code %d", *evt.ExitCode), Timestamp: now})
			} else {
				m.sendLog(ctx, out, LogEntry{Source: "system", Message: "command completed", Timestamp: now})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			m.sendLog(context.Background(), out, LogEntry{Source: "system", Message: fmt.Sprintf("command interrupted: %v", ctx.Err()), Timestamp: time.Now().UTC()})
			return nil
		}
		return fmt.Errorf("read streaming output: %w", err)
	}

	if ctx.Err() != nil {
		m.sendLog(context.Background(), out, LogEntry{Source: "system", Message: fmt.Sprintf("command interrupted: %v", ctx.Err()), Timestamp: time.Now().UTC()})
		return nil
	}

	m.sendLog(ctx, out, LogEntry{Source: "system", Message: "command completed successfully", Timestamp: time.Now().UTC()})
	return nil
}

func (m *E2BSandboxManager) writeFile(ctx context.Context, sandboxID, filePath, content string) error {
	payload := map[string]string{
		"path":    filePath,
		"content": content,
	}
	endpoint := "/sandboxes/" + url.PathEscape(strings.TrimSpace(sandboxID)) + "/files"
	if err := m.doJSON(ctx, http.MethodPost, endpoint, payload, nil); err != nil {
		return err
	}
	return nil
}

func (m *E2BSandboxManager) readFile(ctx context.Context, sandboxID, filePath string) (string, error) {
	endpoint := "/sandboxes/" + url.PathEscape(strings.TrimSpace(sandboxID)) + "/files?path=" + url.QueryEscape(filePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build read file request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+m.apiKey)
	request.Header.Set("Accept", "application/json, text/plain")

	resp, err := m.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("execute read file request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("read file: failed to close response body", "error", closeErr)
		}
	}()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read file response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("read file failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", nil
	}

	var contentResp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &contentResp); err == nil && contentResp.Content != "" {
		return contentResp.Content, nil
	}

	return string(data), nil
}

func (m *E2BSandboxManager) doJSON(ctx context.Context, method, endpoint string, reqBody any, respBody any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request payload: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.baseURL+endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("doJSON: failed to close response body", "error", closeErr)
		}
	}()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if respBody == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func (m *E2BSandboxManager) withSandboxTimeout(ctx context.Context, sb *Sandbox) (context.Context, context.CancelFunc) {
	if sb == nil || sb.ExpiresAt.IsZero() {
		return context.WithCancel(ctx)
	}

	remaining := time.Until(sb.ExpiresAt)
	if remaining <= 0 {
		return context.WithTimeout(ctx, time.Second)
	}

	return context.WithTimeout(ctx, remaining)
}

func (m *E2BSandboxManager) sendLog(ctx context.Context, out chan<- LogEntry, entry LogEntry) bool {
	select {
	case out <- entry:
		return true
	case <-ctx.Done():
		return false
	}
}

func resolveWorkingDir(repoDir, override string) (string, error) {
	base := path.Clean(strings.TrimSpace(repoDir))
	if base == "" || base == "." {
		base = "/workspace/repo"
	}

	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		return base, nil
	}

	if strings.HasPrefix(trimmed, "/") {
		clean := path.Clean(trimmed)
		if clean == "/" || clean == "." {
			return "", errors.New("invalid absolute working directory")
		}
		return clean, nil
	}

	cleanRel := path.Clean(trimmed)
	if cleanRel == "." || cleanRel == "" {
		return base, nil
	}
	if strings.HasPrefix(cleanRel, "../") || cleanRel == ".." {
		return "", fmt.Errorf("working directory %q escapes repository", override)
	}

	return path.Clean(path.Join(base, cleanRel)), nil
}

func resolveSandboxFilePath(workDir, relPath string) (string, error) {
	cleanRel := path.Clean(strings.TrimSpace(relPath))
	if cleanRel == "." || cleanRel == "" {
		return "", errors.New("invalid file path")
	}
	if strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(cleanRel, "../") || cleanRel == ".." {
		return "", fmt.Errorf("path %q escapes sandbox working directory", relPath)
	}

	base := path.Clean(strings.TrimSpace(workDir))
	if base == "" || base == "." {
		base = "/workspace/repo"
	}
	target := path.Clean(path.Join(base, cleanRel))
	if !strings.HasPrefix(target, base+"/") && target != base {
		return "", fmt.Errorf("path %q escapes sandbox working directory", relPath)
	}

	return target, nil
}

func buildCommandString(cmd string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellEscape(cmd))
	for _, arg := range args {
		parts = append(parts, shellEscape(arg))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func shellEscape(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\r\"'`$\\|&;<>*?()[]{}!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func getStringAny(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := lookupAny(data, key)
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if typed != "" {
				return typed
			}
		case json.Number:
			return typed.String()
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		case int:
			return strconv.Itoa(typed)
		}
	}
	return ""
}

func getIntAny(data map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := lookupAny(data, key)
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case float64:
			return int(typed)
		case int:
			return typed
		case int32:
			return int(typed)
		case int64:
			return int(typed)
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return n
			}
		}
	}
	return 0
}

func lookupAny(data map[string]any, key string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(key), ".")
	if len(parts) == 0 {
		return nil, false
	}

	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, exists := m[part]
		if !exists {
			return nil, false
		}
		current = value
	}

	return current, true
}
