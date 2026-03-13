package intercom

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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
)

const (
	baseURL  = "https://api.intercom.io"
	oauthURL = "https://app.intercom.com/oauth"
)

// Client is a native Intercom API client that handles OAuth token exchange
// and authenticated API calls without a Nango proxy.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewClient constructs an Intercom API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthorizeURL returns the URL to redirect users to for Intercom OAuth.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	params := url.Values{
		"client_id":    {c.clientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return oauthURL + "?" + params.Encode()
}

// TokenResponse is the response from Intercom's OAuth token exchange.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// ExchangeCode exchanges an authorization code for an access token.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/eagle/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("intercom.ExchangeCode: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("intercom.ExchangeCode: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("intercom.ExchangeCode: status %d: %s", resp.StatusCode, string(body))
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("intercom.ExchangeCode: decode: %w", err)
	}
	return &tok, nil
}

// Conversation is a minimal representation of an Intercom conversation.
type Conversation struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	State           string `json:"state"`
	WaitingSince    int64  `json:"waiting_since"`
	Source          *ConversationSource `json:"source"`
	Tags            *TagList `json:"tags"`
	ConversationParts *ConversationParts `json:"conversation_parts"`
}

type ConversationSource struct {
	Type    string `json:"type"`
	Body    string `json:"body"`
	Subject string `json:"subject"`
	Author  struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

type TagList struct {
	Tags []Tag `json:"tags"`
}

type Tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ConversationParts struct {
	Parts []ConversationPart `json:"conversation_parts"`
}

type ConversationPart struct {
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
	Author    struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

// ListConversationsResponse is the paginated response from the conversations endpoint.
type ListConversationsResponse struct {
	Conversations []Conversation `json:"conversations"`
	Pages         struct {
		Next    string `json:"next"`
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
		Total   int    `json:"total_pages"`
	} `json:"pages"`
}

// ListConversations fetches conversations from Intercom.
// If sinceTimestamp > 0, only returns conversations updated after that time.
func (c *Client) ListConversations(ctx context.Context, accessToken string, sinceTimestamp int64) ([]Conversation, error) {
	var all []Conversation
	page := 1

	for {
		endpoint := fmt.Sprintf("%s/conversations?per_page=50&page=%d&order=updated_at&sort=desc&display_as=plaintext", baseURL, page)
		if sinceTimestamp > 0 {
			endpoint += fmt.Sprintf("&updated_after=%d", sinceTimestamp)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("intercom.ListConversations: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Intercom-Version", "2.11")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("intercom.ListConversations: request: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("intercom.ListConversations: status %d: %s", resp.StatusCode, string(body))
		}

		var result ListConversationsResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("intercom.ListConversations: decode: %w", err)
		}

		all = append(all, result.Conversations...)

		if result.Pages.Next == "" || page >= result.Pages.Total {
			break
		}
		page++
	}

	return all, nil
}

// GetConversation fetches a single conversation with its parts (messages).
func (c *Client) GetConversation(ctx context.Context, accessToken, conversationID string) (*Conversation, error) {
	endpoint := fmt.Sprintf("%s/conversations/%s?display_as=plaintext", baseURL, conversationID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("intercom.GetConversation: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Intercom-Version", "2.11")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("intercom.GetConversation: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("intercom.GetConversation: status %d: %s", resp.StatusCode, string(body))
	}

	var conv Conversation
	if err := json.Unmarshal(body, &conv); err != nil {
		return nil, fmt.Errorf("intercom.GetConversation: decode: %w", err)
	}
	return &conv, nil
}

// VerifyWebhook verifies an Intercom webhook signature using HMAC-SHA256.
func VerifyWebhook(payload []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	// Intercom sends: X-Hub-Signature: sha1=<hex> (legacy) or hmac-sha256=<hex>
	// We support the HMAC-SHA256 variant.
	sig := strings.TrimPrefix(signature, "sha1=")
	sig = strings.TrimPrefix(sig, "hmac-sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

// ConversationToSignal maps an Intercom conversation to a Neuco Signal.
func ConversationToSignal(conv Conversation, projectID uuid.UUID) domain.Signal {
	occurredAt := time.Now().UTC()
	if conv.CreatedAt > 0 {
		occurredAt = time.Unix(conv.CreatedAt, 0).UTC()
	}

	// Build content from source body + conversation parts.
	var content strings.Builder
	if conv.Source != nil && conv.Source.Body != "" {
		content.WriteString(conv.Source.Body)
	}
	if conv.ConversationParts != nil {
		for _, part := range conv.ConversationParts.Parts {
			if part.Body != "" {
				if content.Len() > 0 {
					content.WriteString("\n\n---\n\n")
				}
				if part.Author.Name != "" {
					fmt.Fprintf(&content, "[%s]: ", part.Author.Name)
				}
				content.WriteString(part.Body)
			}
		}
	}
	if content.Len() == 0 {
		if conv.Title != "" {
			content.WriteString(conv.Title)
		} else {
			fmt.Fprintf(&content, "Intercom conversation %s", conv.ID)
		}
	}

	// Extract tags.
	var tags []string
	if conv.Tags != nil {
		for _, t := range conv.Tags.Tags {
			tags = append(tags, t.Name)
		}
	}

	meta, _ := json.Marshal(map[string]any{
		"conversation_id": conv.ID,
		"title":           conv.Title,
		"state":           conv.State,
		"tags":            tags,
		"provider":        "intercom",
	})

	return domain.Signal{
		ID:         uuid.New(),
		ProjectID:  projectID,
		Source:     domain.SignalSourceIntercom,
		SourceRef:  conv.ID,
		Type:       domain.SignalTypeSupportTicket,
		Content:    content.String(),
		Metadata:   json.RawMessage(meta),
		OccurredAt: occurredAt,
	}
}
