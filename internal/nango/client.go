// Package nango provides a client for the Nango integration platform.
// Nango manages OAuth flows and proxies authenticated API calls to third-party
// providers such as Gong, Intercom, and Slack.
package nango

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is the Nango server client. It is safe for concurrent use.
type Client struct {
	baseURL   string
	secretKey string
	http      *http.Client
}

// Connection represents a Nango connection record — an authenticated link
// between a Neuco project and a third-party provider.
type Connection struct {
	ID                string         `json:"id"`
	ConnectionID      string         `json:"connection_id"`
	ProviderConfigKey string         `json:"provider_config_key"`
	Provider          string         `json:"provider"`
	CreatedAt         time.Time      `json:"created_at"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// NewClient constructs a Client that communicates with the Nango server at
// baseURL using secretKey for server-side authentication.
func NewClient(baseURL, secretKey string) *Client {
	return &Client{
		baseURL:   baseURL,
		secretKey: secretKey,
		http: &http.Client{
			Timeout: 30 * time.Second, // hard cap; per-request timeout via context
		},
	}
}

// CreateConnectSession creates a short-lived Nango Connect session token for
// the given end user. The frontend uses this token (instead of a public key)
// to initialise the Nango frontend SDK and run OAuth flows.
//
// Nango API: POST /connect/sessions
func (c *Client) CreateConnectSession(ctx context.Context, endUserID, endUserEmail, endUserDisplayName string) (string, error) {
	payload := map[string]any{
		"end_user": map[string]string{
			"id":           endUserID,
			"email":        endUserEmail,
			"display_name": endUserDisplayName,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("nango: create connect session: marshal payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/connect/sessions", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("nango: create connect session: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("nango: create connect session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("nango: create connect session: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("nango: create connect session: decode response: %w", err)
	}

	if result.Data.Token == "" {
		return "", fmt.Errorf("nango: create connect session: empty token in response")
	}

	return result.Data.Token, nil
}

// ListConnections returns all Nango connections for the given providerConfigKey.
// Nango API: GET /connections?provider_config_key={key}
func (c *Client) ListConnections(ctx context.Context, providerConfigKey string) ([]Connection, error) {
	endpoint := fmt.Sprintf("%s/connections?provider_config_key=%s",
		c.baseURL,
		url.QueryEscape(providerConfigKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("nango: list connections: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nango: list connections: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nango: list connections: unexpected status %d", resp.StatusCode)
	}

	// Nango returns { "connections": [...] }
	var body struct {
		Connections []Connection `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("nango: list connections: decode response: %w", err)
	}

	return body.Connections, nil
}

// GetConnection returns the Nango connection identified by connectionID and
// providerConfigKey.
// Nango API: GET /connections/{connectionId}?provider_config_key={key}
func (c *Client) GetConnection(ctx context.Context, providerConfigKey, connectionID string) (*Connection, error) {
	endpoint := fmt.Sprintf("%s/connections/%s?provider_config_key=%s",
		c.baseURL,
		url.PathEscape(connectionID),
		url.QueryEscape(providerConfigKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("nango: get connection: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nango: get connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("nango: get connection: not found (provider=%s connection=%s)", providerConfigKey, connectionID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nango: get connection: unexpected status %d", resp.StatusCode)
	}

	var conn Connection
	if err := json.NewDecoder(resp.Body).Decode(&conn); err != nil {
		return nil, fmt.Errorf("nango: get connection: decode response: %w", err)
	}

	return &conn, nil
}

// DeleteConnection removes a Nango connection.
// Nango API: DELETE /connections/{connectionId}?provider_config_key={key}
func (c *Client) DeleteConnection(ctx context.Context, providerConfigKey, connectionID string) error {
	endpoint := fmt.Sprintf("%s/connections/%s?provider_config_key=%s",
		c.baseURL,
		url.PathEscape(connectionID),
		url.QueryEscape(providerConfigKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("nango: delete connection: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nango: delete connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("nango: delete connection: not found (provider=%s connection=%s)", providerConfigKey, connectionID)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("nango: delete connection: unexpected status %d", resp.StatusCode)
	}

	return nil
}

// Proxy forwards a request through the Nango proxy to the provider's API.
// Nango adds the correct OAuth access token automatically, so callers only
// need to supply the logical provider path (e.g. "/v2/calls" for Gong).
//
// method must be a valid HTTP verb. body may be nil for GET/DELETE requests.
// The caller is responsible for closing the response body.
//
// Nango API: {method} /proxy/{path}
// Required headers: Provider-Config-Key, Connection-Id, Authorization.
func (c *Client) Proxy(
	ctx context.Context,
	method string,
	providerConfigKey string,
	connectionID string,
	path string,
	body io.Reader,
) (*http.Response, error) {
	// Nango proxy endpoint: the path must start with /.
	if len(path) == 0 || path[0] != '/' {
		path = "/" + path
	}
	endpoint := fmt.Sprintf("%s/proxy%s", c.baseURL, path)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("nango: proxy: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Provider-Config-Key", providerConfigKey)
	req.Header.Set("Connection-Id", connectionID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nango: proxy: %w", err)
	}

	return resp, nil
}
