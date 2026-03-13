package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/jira"
	"github.com/neuco-ai/neuco/internal/jobs"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
)

// JiraAuthorizeURL handles GET /api/v1/projects/{projectId}/jira/authorize.
// Returns the Atlassian OAuth 2.0 (3LO) authorize URL for the frontend to redirect to.
func JiraAuthorizeURL(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.JiraClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Jira integration not configured")
			return
		}

		projectID := mw.ProjectIDFromCtx(r.Context())

		// Generate a state token to prevent CSRF. Encode projectID in it.
		stateBytes := make([]byte, 16)
		if _, err := rand.Read(stateBytes); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to generate state")
			return
		}
		state := projectID.String() + ":" + hex.EncodeToString(stateBytes)

		redirectURI := d.Config.FrontendURL + "/integrations/jira/callback"

		client := jira.NewClient(d.Config.JiraClientID, d.Config.JiraClientSecret)
		authURL := client.AuthorizeURL(redirectURI, state)

		respondOK(w, r, map[string]string{
			"authorize_url": authURL,
			"state":         state,
		})
	}
}

// JiraCallback handles POST /api/v1/projects/{projectId}/jira/callback.
// Exchanges the OAuth code for an access token, fetches the first accessible
// Jira Cloud site, and creates an integration record.
func JiraCallback(d *Deps) http.HandlerFunc {
	type request struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.JiraClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Jira integration not configured")
			return
		}

		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Code == "" {
			respondErr(w, r, http.StatusBadRequest, "code is required")
			return
		}

		projectID := mw.ProjectIDFromCtx(r.Context())
		redirectURI := d.Config.FrontendURL + "/integrations/jira/callback"

		client := jira.NewClient(d.Config.JiraClientID, d.Config.JiraClientSecret)
		tok, err := client.ExchangeCode(r.Context(), req.Code, redirectURI)
		if err != nil {
			slog.Error("jira callback: token exchange failed", "error", err)
			respondErr(w, r, http.StatusBadGateway, "failed to exchange code with Jira")
			return
		}

		// Fetch accessible Jira Cloud sites to get the cloud ID.
		sites, err := client.GetAccessibleSites(r.Context(), tok.AccessToken)
		if err != nil {
			slog.Error("jira callback: get accessible sites failed", "error", err)
			respondErr(w, r, http.StatusBadGateway, "failed to fetch Jira sites")
			return
		}
		if len(sites) == 0 {
			respondErr(w, r, http.StatusBadRequest, "no accessible Jira sites found")
			return
		}

		// Use the first site by default.
		site := sites[0]

		// Create integration record with the access token and cloud ID stored in config.
		intg := domain.Integration{
			ID:        uuid.New(),
			ProjectID: projectID,
			Provider:  "jira",
			Config: map[string]any{
				"access_token":  tok.AccessToken,
				"refresh_token": tok.RefreshToken,
				"cloud_id":      site.ID,
				"site_name":     site.Name,
				"site_url":      site.URL,
				"connected_at":  time.Now().UTC().Format(time.RFC3339),
			},
			IsActive: true,
		}

		created, err := d.Store.CreateIntegration(r.Context(), intg)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to save integration")
			return
		}

		respondCreated(w, r, created)
	}
}

// JiraDisconnect handles DELETE /api/v1/projects/{projectId}/jira/{integrationId}.
// Removes the native Jira integration.
func JiraDisconnect(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		integrationIDStr := chi.URLParam(r, "integrationId")
		integrationID, err := uuid.Parse(integrationIDStr)
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid integration ID")
			return
		}

		if err := d.Store.DeleteIntegration(r.Context(), projectID, integrationID); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to delete integration")
			return
		}

		respondNoContent(w, r)
	}
}

// TriggerJiraSync handles POST /api/v1/projects/{projectId}/jira/{integrationId}/sync.
// Enqueues a JiraSyncJob for the given integration.
func TriggerJiraSync(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		integrationIDStr := chi.URLParam(r, "integrationId")
		integrationID, err := uuid.Parse(integrationIDStr)
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid integration ID")
			return
		}

		// Verify the integration exists and is active.
		intg, err := d.Store.GetIntegration(r.Context(), projectID, integrationID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "integration not found")
			return
		}
		if !intg.IsActive {
			respondErr(w, r, http.StatusConflict, "integration is not active")
			return
		}

		// Create pipeline.
		runID, taskIDs, err := jobs.CreateJiraSyncPipeline(r.Context(), d.Store, projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create pipeline")
			return
		}

		// Enqueue the sync job.
		_, err = d.River.Insert(r.Context(), jobs.JiraSyncJobArgs{
			ProjectID:     projectID,
			IntegrationID: integrationID,
			RunID:         runID,
			TaskID:        taskIDs[0],
		}, nil)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to enqueue sync job")
			return
		}

		respondOK(w, r, map[string]string{
			"run_id": runID.String(),
			"status": "enqueued",
		})
	}
}

// JiraWebhook handles POST /api/v1/webhooks/jira.
// Receives real-time issue events from Jira.
func JiraWebhook(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "failed to read body")
			return
		}

		// Verify webhook signature if secret is configured.
		if d.Config.JiraWebhookSecret != "" {
			sig := r.Header.Get("X-Hub-Signature")
			if !jira.VerifyWebhook(body, sig, d.Config.JiraWebhookSecret) {
				respondErr(w, r, http.StatusUnauthorized, "invalid webhook signature")
				return
			}
		}

		var event struct {
			WebhookEvent string `json:"webhookEvent"` // "jira:issue_created", "jira:issue_updated"
			Issue        *struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				Fields struct {
					Summary   string `json:"summary"`
					Created   string `json:"created"`
					Status    *struct {
						Name string `json:"name"`
					} `json:"status"`
					Priority *struct {
						Name string `json:"name"`
					} `json:"priority"`
					IssueType *struct {
						Name string `json:"name"`
					} `json:"issuetype"`
					Labels  []string `json:"labels"`
					Project *struct {
						Key string `json:"key"`
					} `json:"project"`
				} `json:"fields"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid webhook payload")
			return
		}

		// We only process issue creation events.
		if event.WebhookEvent != "jira:issue_created" || event.Issue == nil {
			w.WriteHeader(http.StatusOK)
			return
		}

		slog.Info("jira webhook: received issue event",
			"event", event.WebhookEvent,
			"issue_key", event.Issue.Key,
		)

		// Find all active jira integrations and insert the signal for each.
		integrations, err := d.Store.ListActiveIntegrationsInternal(r.Context(), "jira")
		if err != nil {
			slog.Error("jira webhook: list integrations", "error", err)
			w.WriteHeader(http.StatusOK) // ACK to Jira even on error
			return
		}

		createdAt := time.Now().UTC()
		if event.Issue.Fields.Created != "" {
			if parsed, parseErr := time.Parse("2006-01-02T15:04:05.000-0700", event.Issue.Fields.Created); parseErr == nil {
				createdAt = parsed
			}
		}

		for _, intg := range integrations {
			issue := jira.Issue{
				ID:  event.Issue.ID,
				Key: event.Issue.Key,
			}
			issue.Fields.Summary = event.Issue.Fields.Summary
			issue.Fields.Created = event.Issue.Fields.Created
			issue.Fields.Labels = event.Issue.Fields.Labels
			if event.Issue.Fields.Status != nil {
				issue.Fields.Status = &struct {
					Name string `json:"name"`
				}{Name: event.Issue.Fields.Status.Name}
			}
			if event.Issue.Fields.Priority != nil {
				issue.Fields.Priority = &struct {
					Name string `json:"name"`
				}{Name: event.Issue.Fields.Priority.Name}
			}
			if event.Issue.Fields.IssueType != nil {
				issue.Fields.IssueType = &struct {
					Name string `json:"name"`
				}{Name: event.Issue.Fields.IssueType.Name}
			}
			if event.Issue.Fields.Project != nil {
				issue.Fields.Project = &struct {
					Key  string `json:"key"`
					Name string `json:"name"`
				}{Key: event.Issue.Fields.Project.Key}
			}

			sig := jira.IssueToSignal(issue, intg.ProjectID)
			sig.OccurredAt = createdAt

			if _, insertErr := d.Store.InsertSignal(r.Context(), sig); insertErr != nil {
				slog.Error("jira webhook: insert signal",
					"issue_key", event.Issue.Key,
					"project_id", intg.ProjectID,
					"error", insertErr,
				)
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}
}
