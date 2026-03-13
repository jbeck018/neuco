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
	"github.com/neuco-ai/neuco/internal/linear"
	"github.com/neuco-ai/neuco/internal/jobs"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
)

// LinearAuthorizeURL handles GET /api/v1/projects/{projectId}/linear/authorize.
// Returns the Linear OAuth authorize URL for the frontend to redirect to.
func LinearAuthorizeURL(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.LinearClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Linear integration not configured")
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

		redirectURI := d.Config.FrontendURL + "/integrations/linear/callback"

		client := linear.NewClient(d.Config.LinearClientID, d.Config.LinearClientSecret)
		authURL := client.AuthorizeURL(redirectURI, state)

		respondOK(w, r, map[string]string{
			"authorize_url": authURL,
			"state":         state,
		})
	}
}

// LinearCallback handles POST /api/v1/projects/{projectId}/linear/callback.
// Exchanges the OAuth code for an access token and creates an integration record.
func LinearCallback(d *Deps) http.HandlerFunc {
	type request struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.LinearClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Linear integration not configured")
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
		redirectURI := d.Config.FrontendURL + "/integrations/linear/callback"

		client := linear.NewClient(d.Config.LinearClientID, d.Config.LinearClientSecret)
		tok, err := client.ExchangeCode(r.Context(), req.Code, redirectURI)
		if err != nil {
			slog.Error("linear callback: token exchange failed", "error", err)
			respondErr(w, r, http.StatusBadGateway, "failed to exchange code with Linear")
			return
		}

		// Create integration record with the access token stored in config.
		intg := domain.Integration{
			ID:        uuid.New(),
			ProjectID: projectID,
			Provider:  "linear",
			Config: map[string]any{
				"access_token": tok.AccessToken,
				"connected_at": time.Now().UTC().Format(time.RFC3339),
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

// LinearDisconnect handles DELETE /api/v1/projects/{projectId}/linear/{integrationId}.
// Removes the native Linear integration.
func LinearDisconnect(d *Deps) http.HandlerFunc {
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

// TriggerLinearSync handles POST /api/v1/projects/{projectId}/linear/{integrationId}/sync.
// Enqueues a LinearSyncJob for the given integration.
func TriggerLinearSync(d *Deps) http.HandlerFunc {
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
		runID, taskIDs, err := jobs.CreateLinearSyncPipeline(r.Context(), d.Store, projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create pipeline")
			return
		}

		// Enqueue the sync job.
		_, err = d.River.Insert(r.Context(), jobs.LinearSyncJobArgs{
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

// LinearWebhook handles POST /api/v1/webhooks/linear.
// Receives real-time issue events from Linear.
func LinearWebhook(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "failed to read body")
			return
		}

		// Verify webhook signature if secret is configured.
		if d.Config.LinearWebhookSecret != "" {
			sig := r.Header.Get("Linear-Signature")
			if !linear.VerifyWebhook(body, sig, d.Config.LinearWebhookSecret) {
				respondErr(w, r, http.StatusUnauthorized, "invalid webhook signature")
				return
			}
		}

		var event struct {
			Action string `json:"action"` // "create", "update", "remove"
			Type   string `json:"type"`   // "Issue", "Comment", "Project", etc.
			Data   struct {
				ID          string `json:"id"`
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Priority    int    `json:"priority"`
				CreatedAt   string `json:"createdAt"`
				State       *struct {
					Name string `json:"name"`
				} `json:"state"`
				Team *struct {
					Key string `json:"key"`
				} `json:"team"`
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"data"`
			OrganizationId string `json:"organizationId"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid webhook payload")
			return
		}

		// We only process issue creation events.
		if event.Type != "Issue" || event.Action != "create" {
			w.WriteHeader(http.StatusOK)
			return
		}

		slog.Info("linear webhook: received issue event",
			"action", event.Action,
			"issue_id", event.Data.ID,
			"identifier", event.Data.Identifier,
		)

		// Find all active linear integrations and insert the signal for each.
		integrations, err := d.Store.ListActiveIntegrationsInternal(r.Context(), "linear")
		if err != nil {
			slog.Error("linear webhook: list integrations", "error", err)
			w.WriteHeader(http.StatusOK) // ACK to Linear even on error
			return
		}

		createdAt := time.Now().UTC()
		if event.Data.CreatedAt != "" {
			if parsed, parseErr := time.Parse(time.RFC3339, event.Data.CreatedAt); parseErr == nil {
				createdAt = parsed
			}
		}

		stateName := ""
		if event.Data.State != nil {
			stateName = event.Data.State.Name
		}
		teamKey := ""
		if event.Data.Team != nil {
			teamKey = event.Data.Team.Key
		}
		for _, intg := range integrations {
			issue := linear.Issue{
				ID:         event.Data.ID,
				Identifier: event.Data.Identifier,
				Title:      event.Data.Title,
				CreatedAt:  createdAt,
			}
			issue.State.Name = stateName
			issue.Team.Key = teamKey

			sig := linear.IssueToSignal(issue, intg.ProjectID)
			if _, insertErr := d.Store.InsertSignal(r.Context(), sig); insertErr != nil {
				slog.Error("linear webhook: insert signal",
					"issue_id", event.Data.ID,
					"project_id", intg.ProjectID,
					"error", insertErr,
				)
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}
}
