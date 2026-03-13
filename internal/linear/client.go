package linear

import (
	"bytes"
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
	apiURL   = "https://api.linear.app/graphql"
	oauthURL = "https://linear.app/oauth/authorize"
	tokenURL = "https://api.linear.app/oauth/token"
)

// Client is a native Linear API client that handles OAuth token exchange
// and authenticated GraphQL API calls.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewClient constructs a Linear API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthorizeURL returns the URL to redirect users to for Linear OAuth.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	params := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"read"},
		"state":         {state},
		"prompt":        {"consent"},
	}
	return oauthURL + "?" + params.Encode()
}

// TokenResponse is the response from Linear's OAuth token exchange.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// ExchangeCode exchanges an authorization code for an access token.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("linear.ExchangeCode: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear.ExchangeCode: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear.ExchangeCode: status %d: %s", resp.StatusCode, string(body))
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("linear.ExchangeCode: decode: %w", err)
	}
	return &tok, nil
}

// Issue is a minimal representation of a Linear issue.
type Issue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignee *struct {
		Name string `json:"name"`
	} `json:"assignee"`
	Team struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	} `json:"team"`
	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
}

// Comment is a minimal representation of a Linear comment.
type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	User      *struct {
		Name string `json:"name"`
	} `json:"user"`
}

// graphQLRequest is the shape of a GraphQL request body.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// ListIssues fetches issues from Linear using the GraphQL API.
// If sinceTimestamp > 0, only returns issues updated after that time.
func (c *Client) ListIssues(ctx context.Context, accessToken string, sinceTimestamp int64) ([]Issue, error) {
	var all []Issue
	var cursor string

	for {
		filter := map[string]any{}
		if sinceTimestamp > 0 {
			filter["updatedAt"] = map[string]string{
				"gte": time.Unix(sinceTimestamp, 0).UTC().Format(time.RFC3339),
			}
		}

		variables := map[string]any{
			"first":  50,
			"filter": filter,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		query := `query ListIssues($first: Int!, $after: String, $filter: IssueFilter) {
			issues(first: $first, after: $after, filter: $filter, orderBy: updatedAt) {
				nodes {
					id
					identifier
					title
					description
					priority
					createdAt
					updatedAt
					state { name }
					labels { nodes { name } }
					assignee { name }
					team { name key }
					comments { nodes { id body createdAt user { name } } }
				}
				pageInfo {
					hasNextPage
					endCursor
				}
			}
		}`

		result, err := c.graphQL(ctx, accessToken, query, variables)
		if err != nil {
			return nil, fmt.Errorf("linear.ListIssues: %w", err)
		}

		var resp struct {
			Data struct {
				Issues struct {
					Nodes    []Issue `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"issues"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			return nil, fmt.Errorf("linear.ListIssues: decode: %w", err)
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("linear.ListIssues: graphql error: %s", resp.Errors[0].Message)
		}

		all = append(all, resp.Data.Issues.Nodes...)

		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}

	return all, nil
}

func (c *Client) graphQL(ctx context.Context, accessToken, query string, variables map[string]any) (json.RawMessage, error) {
	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// VerifyWebhook verifies a Linear webhook signature using HMAC-SHA256.
// Linear sends the signature in the "Linear-Signature" header.
func VerifyWebhook(payload []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

// IssueToSignal maps a Linear issue to a Neuco Signal.
func IssueToSignal(issue Issue, projectID uuid.UUID) domain.Signal {
	occurredAt := issue.CreatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	// Build content from title + description + comments.
	var content strings.Builder
	content.WriteString(issue.Title)
	if issue.Description != "" {
		content.WriteString("\n\n")
		content.WriteString(issue.Description)
	}
	for _, comment := range issue.Comments.Nodes {
		if comment.Body != "" {
			content.WriteString("\n\n---\n\n")
			if comment.User != nil && comment.User.Name != "" {
				fmt.Fprintf(&content, "[%s]: ", comment.User.Name)
			}
			content.WriteString(comment.Body)
		}
	}

	// Extract labels.
	var labels []string
	for _, l := range issue.Labels.Nodes {
		labels = append(labels, l.Name)
	}

	assigneeName := ""
	if issue.Assignee != nil {
		assigneeName = issue.Assignee.Name
	}

	meta, _ := json.Marshal(map[string]any{
		"issue_id":   issue.ID,
		"identifier": issue.Identifier,
		"title":      issue.Title,
		"state":      issue.State.Name,
		"priority":   issue.Priority,
		"labels":     labels,
		"team":       issue.Team.Key,
		"assignee":   assigneeName,
		"provider":   "linear",
	})

	return domain.Signal{
		ID:         uuid.New(),
		ProjectID:  projectID,
		Source:     domain.SignalSourceLinear,
		SourceRef:  issue.ID,
		Type:       domain.SignalTypeLinearIssue,
		Content:    content.String(),
		Metadata:   json.RawMessage(meta),
		OccurredAt: occurredAt,
	}
}
