package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/email"
	"github.com/neuco-ai/neuco/internal/store"
)

// SendEmailWorker delivers transactional emails via the email.Client (Resend).
type SendEmailWorker struct {
	river.WorkerDefaults[SendEmailJobArgs]
	store  *store.Store
	mailer *email.Client
	jobCtx *JobContext
}

func NewSendEmailWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *SendEmailWorker {
	return &SendEmailWorker{
		store:  s,
		mailer: email.New(cfg.ResendAPIKey, cfg.FrontendURL),
		jobCtx: jobCtx,
	}
}

func (w *SendEmailWorker) Work(ctx context.Context, job *river.Job[SendEmailJobArgs]) error {
	if w.mailer == nil {
		slog.Info("email delivery skipped (no RESEND_API_KEY)", "type", job.Args.EmailType)
		return nil
	}

	slog.Info("sending email", "type", job.Args.EmailType)

	switch job.Args.EmailType {
	case "welcome":
		return w.sendWelcome(ctx, job.Args.Payload)
	case "invite":
		return w.sendInvite(ctx, job.Args.Payload)
	case "pr_created":
		return w.sendPRCreated(ctx, job.Args.Payload)
	case "weekly_digest":
		return w.sendWeeklyDigest(ctx, job.Args.Payload)
	default:
		return fmt.Errorf("unknown email type: %s", job.Args.EmailType)
	}
}

// --- payload types ---

type welcomePayload struct {
	Email    string `json:"email"`
	UserName string `json:"user_name"`
}

type invitePayload struct {
	Email       string `json:"email"`
	InviterName string `json:"inviter_name"`
	OrgName     string `json:"org_name"`
}

type prCreatedPayload struct {
	Email       string `json:"email"`
	ProjectName string `json:"project_name"`
	PRURL       string `json:"pr_url"`
	PRNumber    int    `json:"pr_number"`
	FilesCount  int    `json:"files_count"`
}

// --- dispatch methods ---

func (w *SendEmailWorker) sendWelcome(ctx context.Context, raw json.RawMessage) error {
	var p welcomePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("email.welcome: unmarshal: %w", err)
	}
	return w.mailer.SendWelcome(ctx, p.Email, p.UserName)
}

func (w *SendEmailWorker) sendInvite(ctx context.Context, raw json.RawMessage) error {
	var p invitePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("email.invite: unmarshal: %w", err)
	}
	return w.mailer.SendInvite(ctx, p.Email, p.InviterName, p.OrgName)
}

func (w *SendEmailWorker) sendPRCreated(ctx context.Context, raw json.RawMessage) error {
	var p prCreatedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("email.pr_created: unmarshal: %w", err)
	}
	return w.mailer.SendPRCreated(ctx, email.PRNotification{
		ToEmail:     p.Email,
		ProjectName: p.ProjectName,
		PRURL:       p.PRURL,
		PRNumber:    p.PRNumber,
		FilesCount:  p.FilesCount,
	})
}

func (w *SendEmailWorker) sendWeeklyDigest(ctx context.Context, raw json.RawMessage) error {
	var d email.DigestData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("email.weekly_digest: unmarshal: %w", err)
	}
	return w.mailer.SendWeeklyDigest(ctx, d)
}

// EnqueueEmail is a helper to enqueue a transactional email job.
func EnqueueEmail(ctx context.Context, jobCtx *JobContext, emailType string, payload any) error {
	if jobCtx == nil {
		return nil
	}

	client := jobCtx.Client()
	if client == nil {
		return nil // emails disabled when worker client is not initialized
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("enqueue email: marshal: %w", err)
	}

	_, err = client.Insert(ctx, SendEmailJobArgs{
		EmailType: emailType,
		Payload:   raw,
	}, &river.InsertOpts{Queue: "default"})
	if err != nil {
		return fmt.Errorf("enqueue email: insert: %w", err)
	}

	slog.Info("email job enqueued", "type", emailType)
	return nil
}

// DigestEmailsWorker sends weekly digest emails to all org owners/admins.
type DigestEmailsWorker struct {
	river.WorkerDefaults[DigestEmailsJobArgs]
	store  *store.Store
	mailer *email.Client
}

func NewDigestEmailsWorker(s *store.Store, cfg *config.Config) *DigestEmailsWorker {
	return &DigestEmailsWorker{
		store:  s,
		mailer: email.New(cfg.ResendAPIKey, cfg.FrontendURL),
	}
}

func (w *DigestEmailsWorker) Work(ctx context.Context, _ *river.Job[DigestEmailsJobArgs]) error {
	if w.mailer == nil {
		slog.Info("digest emails skipped (no RESEND_API_KEY)")
		return nil
	}

	orgs, err := w.store.ListAllOrgs(ctx)
	if err != nil {
		return fmt.Errorf("digest emails: list orgs: %w", err)
	}

	for _, org := range orgs {
		if err := w.sendOrgDigest(ctx, org); err != nil {
			slog.Error("failed to send digest for org", "org_id", org.ID, "error", err)
		}
	}
	return nil
}

func (w *DigestEmailsWorker) sendOrgDigest(ctx context.Context, org domain.Organization) error {
	stats, err := w.store.GetOrgWeeklyStats(ctx, org.ID)
	if err != nil {
		return err
	}

	// Skip orgs with zero activity.
	if stats.SignalCount == 0 && stats.CandidateCount == 0 && stats.SpecCount == 0 && stats.PRCount == 0 {
		return nil
	}

	projectStats, err := w.store.GetProjectWeeklyStats(ctx, org.ID)
	if err != nil {
		return err
	}

	var projects []email.DigestProject
	for _, ps := range projectStats {
		if ps.SignalCount > 0 || ps.PRCount > 0 {
			projects = append(projects, email.DigestProject{
				Name:        ps.ProjectName,
				SignalCount: ps.SignalCount,
				PRCount:     ps.PRCount,
			})
		}
	}

	// Fetch top 3 copilot insights from the past week.
	topInsights, err := w.store.GetOrgTopCopilotInsights(ctx, org.ID, 3)
	if err != nil {
		slog.Error("failed to fetch copilot insights for digest", "org_id", org.ID, "error", err)
		// Non-fatal: continue without insights.
	}
	var insights []email.DigestInsight
	for _, i := range topInsights {
		insights = append(insights, email.DigestInsight{
			Content:     i.Content,
			NoteType:    i.NoteType,
			ProjectName: i.ProjectName,
		})
	}

	// Send to all owners and admins of this org who haven't opted out.
	members, err := w.store.ListOrgMembers(ctx, org.ID)
	if err != nil {
		return err
	}

	for _, m := range members {
		if m.Role != "owner" && m.Role != "admin" {
			continue
		}
		if m.Email == "" {
			continue
		}
		if m.DigestOptOut {
			continue
		}

		if err := w.mailer.SendWeeklyDigest(ctx, email.DigestData{
			ToEmail:        m.Email,
			UserName:       m.GitHubLogin,
			OrgName:        org.Name,
			OrgSlug:        org.Slug,
			SignalsCount:   stats.SignalCount,
			CandidateCount: stats.CandidateCount,
			SpecsCount:     stats.SpecCount,
			PRsCount:       stats.PRCount,
			Projects:       projects,
			Insights:       insights,
		}); err != nil {
			slog.Error("failed to send digest email",
				"org_id", org.ID,
				"email", m.Email,
				"error", err,
			)
		}
	}
	return nil
}
