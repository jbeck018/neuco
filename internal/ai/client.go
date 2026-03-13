// Package ai provides a unified abstraction layer over LLM providers (Anthropic
// and OpenAI). The LLMClient wraps raw HTTP calls with proper error handling,
// retries on rate-limit (HTTP 429) with exponential back-off, and structured
// request/response types designed to accommodate a future migration to a
// framework such as Eino (github.com/cloudwego/eino) without changing callers.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Shared types
// ──────────────────────────────────────────────────────────────────────────────

// Message is a single turn in a multi-turn conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolDef describes an Anthropic tool that the LLM may call.
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// ToolCall is a single tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolCallResponse is the parsed response from a ChatWithTools call.
// Either Content or ToolCalls (or both) may be populated depending on the
// model's stop reason.
type ToolCallResponse struct {
	// StopReason is one of: "end_turn", "tool_use", "max_tokens", "stop_sequence"
	StopReason string
	// Content holds any plain-text content blocks returned alongside tool calls.
	Content string
	// ToolCalls holds tool_use content blocks when StopReason == "tool_use".
	ToolCalls []ToolCall
}

// ──────────────────────────────────────────────────────────────────────────────
// LLMClient
// ──────────────────────────────────────────────────────────────────────────────

// LLMClient provides access to language models for the various Neuco
// operations: embedding generation (OpenAI) and chat completion (Anthropic).
//
// All methods respect ctx cancellation. Requests that receive HTTP 429 are
// automatically retried up to maxRetries times using truncated exponential
// back-off with full jitter.
type LLMClient struct {
	AnthropicKey string
	OpenAIKey    string
	httpClient   *http.Client
	aiBreaker    *circuitBreaker
}

const (
	maxRetries       = 5
	retryBaseMs      = 500  // milliseconds
	retryMaxMs       = 30_000 // 30 s cap
	anthropicVersion = "2023-06-01"
	embeddingModel   = "text-embedding-3-small"
	embeddingDims    = 1536
	sonnetModel      = "claude-sonnet-4-5"
	haikuModel       = "claude-haiku-4-5-20251001"
)

// NewLLMClient constructs a client. Both keys are optional; callers that omit
// a key receive zero-value / stub responses with a warning logged.
func NewLLMClient(anthropicKey, openAIKey string) *LLMClient {
	return &LLMClient{
		AnthropicKey: anthropicKey,
		OpenAIKey:    openAIKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		aiBreaker: newCircuitBreaker(5, 30*time.Second),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Embedding
// ──────────────────────────────────────────────────────────────────────────────

// GenerateEmbedding calls OpenAI text-embedding-3-small for a single text and
// returns a 1536-dimensional float32 slice.
func (c *LLMClient) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	if c.OpenAIKey == "" {
		slog.Warn("ai: no OpenAI key configured, returning zero embedding")
		return make([]float32, embeddingDims), nil
	}

	payload := map[string]interface{}{
		"model": embeddingModel,
		"input": text,
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := c.doOpenAI(ctx, "POST", "/v1/embeddings", payload, &result); err != nil {
		return nil, fmt.Errorf("ai.GenerateEmbedding: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("ai.GenerateEmbedding: empty data array in response")
	}
	return result.Data[0].Embedding, nil
}

// GenerateEmbeddingBatch calls OpenAI with a slice of strings and returns one
// embedding per input in the same order. OpenAI's batch input accepts a string
// slice directly so this is a single HTTP round-trip.
func (c *LLMClient) GenerateEmbeddingBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	if c.OpenAIKey == "" {
		slog.Warn("ai: no OpenAI key configured, returning zero embeddings")
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = make([]float32, embeddingDims)
		}
		return out, nil
	}

	payload := map[string]interface{}{
		"model": embeddingModel,
		"input": texts,
	}

	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := c.doOpenAI(ctx, "POST", "/v1/embeddings", payload, &result); err != nil {
		return nil, fmt.Errorf("ai.GenerateEmbeddingBatch: %w", err)
	}

	// Preserve original order — OpenAI preserves it but we guard anyway.
	out := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	// Fill any gaps with zero vectors.
	for i, v := range out {
		if v == nil {
			out[i] = make([]float32, embeddingDims)
		}
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Chat
// ──────────────────────────────────────────────────────────────────────────────

// ChatSonnet sends a single-turn system+user prompt to Claude Sonnet and
// returns the first text content block.
func (c *LLMClient) ChatSonnet(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	text, _, err := c.chatClaude(ctx, sonnetModel, systemPrompt, userPrompt, maxTokens)
	return text, err
}

// ChatSonnetWithUsage is like ChatSonnet but also returns LLM call info.
func (c *LLMClient) ChatSonnetWithUsage(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, *LLMCallInfo, error) {
	return c.chatClaude(ctx, sonnetModel, systemPrompt, userPrompt, maxTokens)
}

// ChatHaiku sends a single-turn system+user prompt to Claude Haiku and
// returns the first text content block.
func (c *LLMClient) ChatHaiku(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	text, _, err := c.chatClaude(ctx, haikuModel, systemPrompt, userPrompt, maxTokens)
	return text, err
}

// ChatHaikuWithUsage is like ChatHaiku but also returns LLM call info.
func (c *LLMClient) ChatHaikuWithUsage(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, *LLMCallInfo, error) {
	return c.chatClaude(ctx, haikuModel, systemPrompt, userPrompt, maxTokens)
}

func (c *LLMClient) chatClaude(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int) (string, *LLMCallInfo, error) {
	if c.AnthropicKey == "" {
		slog.Warn("ai: no Anthropic key configured, returning empty response")
		return "", nil, nil
	}

	if maxTokens <= 0 {
		maxTokens = 1024
	}

	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userPrompt},
		},
	}

	start := time.Now()
	var resp anthropicMessagesResponse
	if err := c.doAnthropic(ctx, "POST", "/v1/messages", payload, &resp); err != nil {
		return "", nil, fmt.Errorf("ai.%s: %w", model, err)
	}

	info := &LLMCallInfo{
		Model:     model,
		TokensIn:  resp.Usage.InputTokens,
		TokensOut: resp.Usage.OutputTokens,
		LatencyMs: int(time.Since(start).Milliseconds()),
	}

	return resp.firstText(), info, nil
}

// ChatWithTools sends a multi-turn conversation with tool definitions to
// Claude. The caller is responsible for managing the conversation loop; this
// method executes a single API round-trip. It supports the Anthropic tool_use
// / tool_result content-block protocol.
func (c *LLMClient) ChatWithTools(
	ctx context.Context,
	model string,
	systemPrompt string,
	messages []Message,
	tools []ToolDef,
	maxTokens int,
) (*ToolCallResponse, *LLMCallInfo, error) {
	if c.AnthropicKey == "" {
		slog.Warn("ai: no Anthropic key configured, returning empty tool response")
		return &ToolCallResponse{StopReason: "end_turn"}, nil, nil
	}

	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Convert our Message slice to the Anthropic wire format.
	apiMessages := make([]map[string]interface{}, len(messages))
	for i, m := range messages {
		apiMessages[i] = map[string]interface{}{
			"role":    m.Role,
			"content": m.Content,
		}
	}

	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages":   apiMessages,
		"tools":      tools,
	}

	start := time.Now()
	var resp anthropicMessagesResponse
	if err := c.doAnthropic(ctx, "POST", "/v1/messages", payload, &resp); err != nil {
		return nil, nil, fmt.Errorf("ai.ChatWithTools: %w", err)
	}

	info := &LLMCallInfo{
		Model:     model,
		TokensIn:  resp.Usage.InputTokens,
		TokensOut: resp.Usage.OutputTokens,
		LatencyMs: int(time.Since(start).Milliseconds()),
	}

	tcr := &ToolCallResponse{
		StopReason: resp.StopReason,
		Content:    resp.firstText(),
	}
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			tcr.ToolCalls = append(tcr.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	return tcr, info, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal wire types
// ──────────────────────────────────────────────────────────────────────────────

type anthropicMessagesResponse struct {
	ID         string `json:"id"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// LLMCallInfo holds usage details from a completed LLM API call.
type LLMCallInfo struct {
	Model     string
	TokensIn  int
	TokensOut int
	LatencyMs int
}

func (r anthropicMessagesResponse) firstText() string {
	for _, b := range r.Content {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// ──────────────────────────────────────────────────────────────────────────────
// HTTP helpers
// ──────────────────────────────────────────────────────────────────────────────

const (
	aiCallTimeout = 30 * time.Second
)

func (c *LLMClient) doAnthropic(ctx context.Context, method, path string, payload, out interface{}) error {
	if !c.aiBreaker.allow() {
		return ErrCircuitOpen
	}

	ctx, cancel := context.WithTimeout(ctx, aiCallTimeout)
	defer cancel()

	err := c.doWithRetry(ctx, method, "https://api.anthropic.com"+path, payload, out,
		func(req *http.Request) {
			req.Header.Set("x-api-key", c.AnthropicKey)
			req.Header.Set("anthropic-version", anthropicVersion)
			req.Header.Set("Content-Type", "application/json")
		},
	)
	if err != nil {
		c.aiBreaker.recordFailure()
		return err
	}
	c.aiBreaker.recordSuccess()
	return nil
}

func (c *LLMClient) doOpenAI(ctx context.Context, method, path string, payload, out interface{}) error {
	if !c.aiBreaker.allow() {
		return ErrCircuitOpen
	}

	ctx, cancel := context.WithTimeout(ctx, aiCallTimeout)
	defer cancel()

	err := c.doWithRetry(ctx, method, "https://api.openai.com"+path, payload, out,
		func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+c.OpenAIKey)
			req.Header.Set("Content-Type", "application/json")
		},
	)
	if err != nil {
		c.aiBreaker.recordFailure()
		return err
	}
	c.aiBreaker.recordSuccess()
	return nil
}

// doWithRetry serialises payload to JSON, sends the request with setHeaders
// applied, and decodes the response body into out. On HTTP 429 it retries with
// truncated exponential back-off + full jitter up to maxRetries times.
func (c *LLMClient) doWithRetry(
	ctx context.Context,
	method, url string,
	payload interface{},
	out interface{},
	setHeaders func(*http.Request),
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Truncated exponential back-off with full jitter.
			capMs := math.Min(float64(retryMaxMs), float64(retryBaseMs)*math.Pow(2, float64(attempt-1)))
			sleepMs := rand.Int63n(int64(capMs) + 1)
			slog.Info("ai: rate limited, retrying",
				"attempt", attempt,
				"sleep_ms", sleepMs,
				"url", url,
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(sleepMs) * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		setHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http do: %w", err)
			// Don't retry on network errors (context cancelled etc.).
			return lastErr
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("rate limited (429) from %s", url)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, string(errBody))
		}

		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("decode response: %w", err)
		}
		_ = resp.Body.Close()
		return nil
	}

	return fmt.Errorf("exhausted %d retries: %w", maxRetries, lastErr)
}
