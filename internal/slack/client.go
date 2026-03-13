package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
)

const (
	oauthAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	oauthAccessURL    = "https://slack.com/api/oauth.v2.access"
	apiBaseURL        = "https://slack.com/api"
)

// Scopes required for signal ingestion (reading channel messages) and
// bot notifications (posting to channels).
var BotScopes = []string{
	"channels:history",
	"channels:read",
	"chat:write",
	"groups:history",
	"groups:read",
}

// Client is a native Slack API client that handles OAuth token exchange
// and authenticated API calls.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewClient constructs a Slack API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthorizeURL returns the URL to redirect users to for Slack OAuth.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	params := url.Values{
		"client_id":    {c.clientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
		"scope":        {strings.Join(BotScopes, ",")},
	}
	return oauthAuthorizeURL + "?" + params.Encode()
}

// OAuthV2Response is the response from Slack's oauth.v2.access endpoint.
type OAuthV2Response struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	BotUserID   string `json:"bot_user_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AuthedUser struct {
		ID string `json:"id"`
	} `json:"authed_user"`
}

// ExchangeCode exchanges an authorization code for a bot access token.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*OAuthV2Response, error) {
	data := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthAccessURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("slack.ExchangeCode: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack.ExchangeCode: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack.ExchangeCode: status %d: %s", resp.StatusCode, string(body))
	}

	var result OAuthV2Response
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("slack.ExchangeCode: decode: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("slack.ExchangeCode: slack error: %s", result.Error)
	}
	return &result, nil
}

// Channel represents a Slack channel.
type Channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsMember   bool   `json:"is_member"`
	IsPrivate  bool   `json:"is_private"`
	IsArchived bool   `json:"is_archived"`
}

// Message represents a Slack message.
type Message struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Text    string `json:"text"`
	User    string `json:"user"`
	TS      string `json:"ts"`
}

// ListChannels fetches public channels the bot is a member of.
func ListChannels(ctx context.Context, accessToken string) ([]Channel, error) {
	var all []Channel
	cursor := ""

	for {
		endpoint := apiBaseURL + "/conversations.list?types=public_channel,private_channel&exclude_archived=true&limit=200"
		if cursor != "" {
			endpoint += "&cursor=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("slack.ListChannels: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("slack.ListChannels: request: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()

		var result struct {
			OK       bool      `json:"ok"`
			Error    string    `json:"error,omitempty"`
			Channels []Channel `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("slack.ListChannels: decode: %w", err)
		}
		if !result.OK {
			return nil, fmt.Errorf("slack.ListChannels: slack error: %s", result.Error)
		}

		for _, ch := range result.Channels {
			if ch.IsMember {
				all = append(all, ch)
			}
		}

		if result.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = result.ResponseMetadata.NextCursor
	}

	return all, nil
}

// FetchChannelHistory fetches messages from a Slack channel.
// If oldest > 0, only fetches messages after that Unix timestamp.
func FetchChannelHistory(ctx context.Context, accessToken, channelID string, oldest float64) ([]Message, error) {
	var all []Message
	cursor := ""

	for {
		endpoint := apiBaseURL + "/conversations.history?channel=" + url.QueryEscape(channelID) + "&limit=200"
		if oldest > 0 {
			endpoint += "&oldest=" + strconv.FormatFloat(oldest, 'f', 6, 64)
		}
		if cursor != "" {
			endpoint += "&cursor=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("slack.FetchChannelHistory: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("slack.FetchChannelHistory: request: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()

		var result struct {
			OK       bool      `json:"ok"`
			Error    string    `json:"error,omitempty"`
			Messages []Message `json:"messages"`
			HasMore  bool      `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("slack.FetchChannelHistory: decode: %w", err)
		}
		if !result.OK {
			return nil, fmt.Errorf("slack.FetchChannelHistory: slack error: %s", result.Error)
		}

		// Filter out bot messages and subtypes (joins, leaves, etc.).
		for _, msg := range result.Messages {
			if msg.Subtype == "" && msg.Text != "" {
				all = append(all, msg)
			}
		}

		if !result.HasMore || result.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = result.ResponseMetadata.NextCursor
	}

	return all, nil
}

// PostMessage sends a message to a Slack channel.
func PostMessage(ctx context.Context, accessToken, channelID, text string) error {
	payload, _ := json.Marshal(map[string]string{
		"channel": channelID,
		"text":    text,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/chat.postMessage", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("slack.PostMessage: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack.PostMessage: request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("slack.PostMessage: decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack.PostMessage: slack error: %s", result.Error)
	}
	return nil
}

// VerifyWebhook verifies a Slack webhook request using the signing secret.
// Slack sends: X-Slack-Signature (v0=<hex>) and X-Slack-Request-Timestamp.
func VerifyWebhook(body []byte, timestamp, signature, signingSecret string) bool {
	if signingSecret == "" || signature == "" || timestamp == "" {
		return false
	}

	// Reject requests older than 5 minutes to prevent replay attacks.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if abs(time.Now().Unix()-ts) > 300 {
		return false
	}

	// Compute HMAC-SHA256 of "v0:<timestamp>:<body>"
	baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(baseString))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// MessageToSignal converts a Slack message to a Neuco Signal.
func MessageToSignal(msg Message, channelName, channelID string, projectID uuid.UUID) domain.Signal {
	ts, _ := strconv.ParseFloat(msg.TS, 64)
	occurredAt := time.Now().UTC()
	if ts > 0 {
		occurredAt = time.Unix(int64(ts), 0).UTC()
	}

	meta, _ := json.Marshal(map[string]any{
		"channel_id":   channelID,
		"channel_name": channelName,
		"user_id":      msg.User,
		"ts":           msg.TS,
		"provider":     "slack",
	})

	return domain.Signal{
		ID:         uuid.New(),
		ProjectID:  projectID,
		Source:     domain.SignalSourceSlack,
		SourceRef:  channelID + ":" + msg.TS,
		Type:       domain.SignalTypeSlackMessage,
		Content:    msg.Text,
		Metadata:   json.RawMessage(meta),
		OccurredAt: occurredAt,
	}
}
