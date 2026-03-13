package jira

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
	oauthURL = "https://auth.atlassian.com/authorize"
	tokenURL = "https://auth.atlassian.com/oauth/token"
	apiBase  = "https://api.atlassian.com"
)

// Client is a native Jira Cloud API client that handles OAuth 2.0 (3LO)
// token exchange and authenticated REST API calls.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewClient constructs a Jira API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthorizeURL returns the Atlassian OAuth 2.0 (3LO) authorize URL.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	params := url.Values{
		"audience":      {"api.atlassian.com"},
		"client_id":     {c.clientID},
		"scope":         {"read:jira-work read:jira-user offline_access"},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"response_type": {"code"},
		"prompt":        {"consent"},
	}
	return oauthURL + "?" + params.Encode()
}

// TokenResponse is the response from Atlassian's OAuth token exchange.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// ExchangeCode exchanges an authorization code for an access token.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jira.ExchangeCode: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("jira.ExchangeCode: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jira.ExchangeCode: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jira.ExchangeCode: status %d: %s", resp.StatusCode, string(respBody))
	}

	var tok TokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("jira.ExchangeCode: decode: %w", err)
	}
	return &tok, nil
}

// CloudSite represents an accessible Jira Cloud site.
type CloudSite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// GetAccessibleSites returns the Jira Cloud sites accessible with the given token.
func (c *Client) GetAccessibleSites(ctx context.Context, accessToken string) ([]CloudSite, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/oauth/token/accessible-resources", nil)
	if err != nil {
		return nil, fmt.Errorf("jira.GetAccessibleSites: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jira.GetAccessibleSites: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jira.GetAccessibleSites: status %d: %s", resp.StatusCode, string(body))
	}

	var sites []CloudSite
	if err := json.Unmarshal(body, &sites); err != nil {
		return nil, fmt.Errorf("jira.GetAccessibleSites: decode: %w", err)
	}
	return sites, nil
}

// Issue is a minimal representation of a Jira issue.
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description *struct {
			Content []json.RawMessage `json:"content"`
		} `json:"description"`
		Status *struct {
			Name string `json:"name"`
		} `json:"status"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Assignee *struct {
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Reporter *struct {
			DisplayName string `json:"displayName"`
		} `json:"reporter"`
		Labels  []string `json:"labels"`
		Created string   `json:"created"`
		Updated string   `json:"updated"`
		Project *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"project"`
		Comment *struct {
			Comments []Comment `json:"comments"`
			Total    int       `json:"total"`
		} `json:"comment"`
	} `json:"fields"`
}

// Comment is a minimal representation of a Jira comment.
type Comment struct {
	ID      string `json:"id"`
	Body    *struct {
		Content []json.RawMessage `json:"content"`
	} `json:"body"`
	Author *struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string `json:"created"`
}

// searchResponse is the Jira search API response.
type searchResponse struct {
	StartAt    int     `json:"startAt"`
	MaxResults int     `json:"maxResults"`
	Total      int     `json:"total"`
	Issues     []Issue `json:"issues"`
}

// ListIssues fetches issues from a Jira Cloud site using the REST API.
// If sinceTimestamp > 0, only returns issues updated after that time.
func (c *Client) ListIssues(ctx context.Context, accessToken, cloudID string, sinceTimestamp int64) ([]Issue, error) {
	var all []Issue
	startAt := 0
	maxResults := 50

	for {
		jql := "ORDER BY updated DESC"
		if sinceTimestamp > 0 {
			since := time.Unix(sinceTimestamp, 0).UTC().Format("2006-01-02 15:04")
			jql = fmt.Sprintf("updated >= '%s' ORDER BY updated DESC", since)
		}

		params := url.Values{
			"jql":        {jql},
			"startAt":    {fmt.Sprintf("%d", startAt)},
			"maxResults": {fmt.Sprintf("%d", maxResults)},
			"fields":     {"summary,description,status,priority,issuetype,assignee,reporter,labels,created,updated,project,comment"},
		}

		endpoint := fmt.Sprintf("%s/ex/jira/%s/rest/api/3/search?%s", apiBase, cloudID, params.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("jira.ListIssues: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jira.ListIssues: request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("jira.ListIssues: status %d: %s", resp.StatusCode, string(body))
		}

		var result searchResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("jira.ListIssues: decode: %w", err)
		}

		all = append(all, result.Issues...)

		if startAt+len(result.Issues) >= result.Total {
			break
		}
		startAt += len(result.Issues)
	}

	return all, nil
}

// VerifyWebhook verifies a Jira webhook signature using HMAC-SHA256.
func VerifyWebhook(payload []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

// extractTextFromADF extracts plain text from Atlassian Document Format content.
func extractTextFromADF(content []json.RawMessage) string {
	var parts []string
	for _, node := range content {
		var n struct {
			Type    string            `json:"type"`
			Text    string            `json:"text"`
			Content []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(node, &n); err != nil {
			continue
		}
		if n.Text != "" {
			parts = append(parts, n.Text)
		}
		if len(n.Content) > 0 {
			if nested := extractTextFromADF(n.Content); nested != "" {
				parts = append(parts, nested)
			}
		}
	}
	return strings.Join(parts, " ")
}

// IssueToSignal maps a Jira issue to a Neuco Signal.
func IssueToSignal(issue Issue, projectID uuid.UUID) domain.Signal {
	createdAt := time.Now().UTC()
	if issue.Fields.Created != "" {
		if parsed, err := time.Parse("2006-01-02T15:04:05.000-0700", issue.Fields.Created); err == nil {
			createdAt = parsed
		}
	}

	// Build content from summary + description + comments.
	var content strings.Builder
	content.WriteString(issue.Fields.Summary)

	if issue.Fields.Description != nil && len(issue.Fields.Description.Content) > 0 {
		desc := extractTextFromADF(issue.Fields.Description.Content)
		if desc != "" {
			content.WriteString("\n\n")
			content.WriteString(desc)
		}
	}

	if issue.Fields.Comment != nil {
		for _, comment := range issue.Fields.Comment.Comments {
			if comment.Body != nil && len(comment.Body.Content) > 0 {
				commentText := extractTextFromADF(comment.Body.Content)
				if commentText != "" {
					content.WriteString("\n\n---\n\n")
					if comment.Author != nil && comment.Author.DisplayName != "" {
						fmt.Fprintf(&content, "[%s]: ", comment.Author.DisplayName)
					}
					content.WriteString(commentText)
				}
			}
		}
	}

	statusName := ""
	if issue.Fields.Status != nil {
		statusName = issue.Fields.Status.Name
	}
	priorityName := ""
	if issue.Fields.Priority != nil {
		priorityName = issue.Fields.Priority.Name
	}
	issueTypeName := ""
	if issue.Fields.IssueType != nil {
		issueTypeName = issue.Fields.IssueType.Name
	}
	assigneeName := ""
	if issue.Fields.Assignee != nil {
		assigneeName = issue.Fields.Assignee.DisplayName
	}
	projectKey := ""
	if issue.Fields.Project != nil {
		projectKey = issue.Fields.Project.Key
	}

	meta, _ := json.Marshal(map[string]any{
		"issue_id":   issue.ID,
		"issue_key":  issue.Key,
		"title":      issue.Fields.Summary,
		"status":     statusName,
		"priority":   priorityName,
		"issue_type": issueTypeName,
		"labels":     issue.Fields.Labels,
		"assignee":   assigneeName,
		"project_key": projectKey,
		"provider":   "jira",
	})

	return domain.Signal{
		ID:         uuid.New(),
		ProjectID:  projectID,
		Source:     domain.SignalSourceJira,
		SourceRef:  issue.Key,
		Type:       domain.SignalTypeJiraIssue,
		Content:    content.String(),
		Metadata:   json.RawMessage(meta),
		OccurredAt: createdAt,
	}
}
