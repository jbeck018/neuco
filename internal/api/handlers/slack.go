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
	"github.com/neuco-ai/neuco/internal/jobs"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/slack"
)

// SlackAuthorizeURL handles GET /api/v1/projects/{projectId}/slack/authorize.
// Returns the Slack OAuth authorize URL for the frontend to redirect to.
func SlackAuthorizeURL(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.SlackClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Slack integration not configured")
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

		redirectURI := d.Config.FrontendURL + "/integrations/slack/callback"

		client := slack.NewClient(d.Config.SlackClientID, d.Config.SlackClientSecret)
		authURL := client.AuthorizeURL(redirectURI, state)

		respondOK(w, r, map[string]string{
			"authorize_url": authURL,
			"state":         state,
		})
	}
}

// SlackCallback handles POST /api/v1/projects/{projectId}/slack/callback.
// Exchanges the OAuth code for an access token and creates an integration record.
func SlackCallback(d *Deps) http.HandlerFunc {
	type request struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.SlackClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Slack integration not configured")
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
		redirectURI := d.Config.FrontendURL + "/integrations/slack/callback"

		client := slack.NewClient(d.Config.SlackClientID, d.Config.SlackClientSecret)
		tok, err := client.ExchangeCode(r.Context(), req.Code, redirectURI)
		if err != nil {
			slog.Error("slack callback: token exchange failed", "error", err)
			respondErr(w, r, http.StatusBadGateway, "failed to exchange code with Slack")
			return
		}

		// Create integration record with the bot access token stored in config.
		intg := domain.Integration{
			ID:        uuid.New(),
			ProjectID: projectID,
			Provider:  "slack",
			Config: map[string]any{
				"access_token": tok.AccessToken,
				"team_id":      tok.Team.ID,
				"team_name":    tok.Team.Name,
				"bot_user_id":  tok.BotUserID,
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

// SlackDisconnect handles DELETE /api/v1/projects/{projectId}/slack/{integrationId}.
// Removes the native Slack integration.
func SlackDisconnect(d *Deps) http.HandlerFunc {
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

// TriggerSlackSync handles POST /api/v1/projects/{projectId}/slack/{integrationId}/sync.
// Enqueues a SlackSyncJob for the given integration.
func TriggerSlackSync(d *Deps) http.HandlerFunc {
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
		runID, taskIDs, err := jobs.CreateSlackSyncPipeline(r.Context(), d.Store, projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create pipeline")
			return
		}

		// Enqueue the sync job.
		_, err = d.River.Insert(r.Context(), jobs.SlackSyncJobArgs{
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

// SlackWebhook handles POST /api/v1/webhooks/slack.
// Receives Events API events from Slack (url_verification + event_callback).
func SlackWebhook(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "failed to read body")
			return
		}

		// Verify request signature if signing secret is configured.
		if d.Config.SlackSigningSecret != "" {
			timestamp := r.Header.Get("X-Slack-Request-Timestamp")
			signature := r.Header.Get("X-Slack-Signature")
			if !slack.VerifyWebhook(body, timestamp, signature, d.Config.SlackSigningSecret) {
				respondErr(w, r, http.StatusUnauthorized, "invalid webhook signature")
				return
			}
		}

		// Parse the outer envelope to determine event type.
		var envelope struct {
			Type      string `json:"type"`
			Token     string `json:"token"`
			Challenge string `json:"challenge"`
			Event     struct {
				Type    string `json:"type"`
				Channel string `json:"channel"`
				User    string `json:"user"`
				Text    string `json:"text"`
				TS      string `json:"ts"`
				Subtype string `json:"subtype,omitempty"`
			} `json:"event"`
			TeamID string `json:"team_id"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid webhook payload")
			return
		}

		// Handle Slack URL verification challenge.
		if envelope.Type == "url_verification" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
			return
		}

		// We only process message events (not subtypes like bot_message, join, etc.).
		if envelope.Type != "event_callback" || envelope.Event.Type != "message" || envelope.Event.Subtype != "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		slog.Info("slack webhook: received message event",
			"channel", envelope.Event.Channel,
			"user", envelope.Event.User,
			"ts", envelope.Event.TS,
		)

		// Find all active Slack integrations and insert the signal for each.
		integrations, err := d.Store.ListActiveIntegrationsInternal(r.Context(), "slack")
		if err != nil {
			slog.Error("slack webhook: list integrations", "error", err)
			w.WriteHeader(http.StatusOK) // ACK to Slack even on error
			return
		}

		for _, intg := range integrations {
			msg := slack.Message{
				Text: envelope.Event.Text,
				User: envelope.Event.User,
				TS:   envelope.Event.TS,
			}
			sig := slack.MessageToSignal(msg, "", envelope.Event.Channel, intg.ProjectID)
			if _, insertErr := d.Store.InsertSignal(r.Context(), sig); insertErr != nil {
				slog.Error("slack webhook: insert signal",
					"channel", envelope.Event.Channel,
					"project_id", intg.ProjectID,
					"error", insertErr,
				)
			}
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	}
}
