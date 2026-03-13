package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const resendAPI = "https://api.resend.com/emails"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Client sends transactional emails via the Resend API.
type Client struct {
	apiKey      string
	fromAddress string
	frontendURL string
}

// New creates a Resend email client. Returns nil if apiKey is empty (emails disabled).
func New(apiKey, frontendURL string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{
		apiKey:      apiKey,
		fromAddress: "Neuco <noreply@neuco.ai>",
		frontendURL: frontendURL,
	}
}

// Message represents an outbound email.
type Message struct {
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text,omitempty"`
}

// Send delivers an email via Resend. Returns nil on success.
func (c *Client) Send(ctx context.Context, msg Message) error {
	payload := map[string]interface{}{
		"from":    c.fromAddress,
		"to":      msg.To,
		"subject": msg.Subject,
		"html":    msg.HTML,
	}
	if msg.Text != "" {
		payload["text"] = msg.Text
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("email: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", resendAPI, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("email: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("email: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("email: resend returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.InfoContext(ctx, "email sent",
		"to", msg.To,
		"subject", msg.Subject,
	)
	return nil
}

// SendWelcome sends a welcome email to a newly signed-up user.
func (c *Client) SendWelcome(ctx context.Context, toEmail, userName string) error {
	html := renderWelcome(userName, c.frontendURL)
	return c.Send(ctx, Message{
		To:      []string{toEmail},
		Subject: "Welcome to Neuco!",
		HTML:    html,
	})
}

// SendInvite sends a team invite email.
func (c *Client) SendInvite(ctx context.Context, toEmail, inviterName, orgName string) error {
	html := renderInvite(inviterName, orgName, c.frontendURL)
	return c.Send(ctx, Message{
		To:      []string{toEmail},
		Subject: fmt.Sprintf("You've been invited to %s on Neuco", orgName),
		HTML:    html,
	})
}

// PRNotification holds the data for a PR-created email.
type PRNotification struct {
	ToEmail     string
	ProjectName string
	PRURL       string
	PRNumber    int
	FilesCount  int
}

// SendPRCreated sends a notification that a GitHub PR was created.
func (c *Client) SendPRCreated(ctx context.Context, n PRNotification) error {
	html := renderPRCreated(n, c.frontendURL)
	return c.Send(ctx, Message{
		To:      []string{n.ToEmail},
		Subject: fmt.Sprintf("Neuco: PR #%d created for %s", n.PRNumber, n.ProjectName),
		HTML:    html,
	})
}

// DigestData holds aggregate data for a weekly digest email.
type DigestData struct {
	ToEmail        string
	UserName       string
	OrgName        string
	OrgSlug        string
	SignalsCount   int
	CandidateCount int
	SpecsCount     int
	PRsCount       int
	Projects       []DigestProject
	Insights       []DigestInsight
}

// DigestProject holds per-project stats for digest.
type DigestProject struct {
	Name        string
	SignalCount int
	PRCount     int
}

// DigestInsight holds a copilot note for the digest email.
type DigestInsight struct {
	Content     string
	NoteType    string
	ProjectName string
}

// SendWeeklyDigest sends the weekly activity summary.
func (c *Client) SendWeeklyDigest(ctx context.Context, d DigestData) error {
	html := renderWeeklyDigest(d, c.frontendURL)
	return c.Send(ctx, Message{
		To:      []string{d.ToEmail},
		Subject: fmt.Sprintf("Neuco Weekly: %s activity summary", d.OrgName),
		HTML:    html,
	})
}
