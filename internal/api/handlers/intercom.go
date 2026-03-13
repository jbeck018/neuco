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
	"github.com/neuco-ai/neuco/internal/intercom"
	"github.com/neuco-ai/neuco/internal/jobs"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
)

// IntercomAuthorizeURL handles GET /api/v1/projects/{projectId}/intercom/authorize.
// Returns the Intercom OAuth authorize URL for the frontend to redirect to.
func IntercomAuthorizeURL(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.IntercomClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Intercom integration not configured")
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

		redirectURI := d.Config.FrontendURL + "/integrations/intercom/callback"

		client := intercom.NewClient(d.Config.IntercomClientID, d.Config.IntercomClientSecret)
		authURL := client.AuthorizeURL(redirectURI, state)

		respondOK(w, r, map[string]string{
			"authorize_url": authURL,
			"state":         state,
		})
	}
}

// IntercomCallback handles POST /api/v1/projects/{projectId}/intercom/callback.
// Exchanges the OAuth code for an access token and creates an integration record.
func IntercomCallback(d *Deps) http.HandlerFunc {
	type request struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if d.Config.IntercomClientID == "" {
			respondErr(w, r, http.StatusServiceUnavailable, "Intercom integration not configured")
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
		redirectURI := d.Config.FrontendURL + "/integrations/intercom/callback"

		client := intercom.NewClient(d.Config.IntercomClientID, d.Config.IntercomClientSecret)
		tok, err := client.ExchangeCode(r.Context(), req.Code, redirectURI)
		if err != nil {
			slog.Error("intercom callback: token exchange failed", "error", err)
			respondErr(w, r, http.StatusBadGateway, "failed to exchange code with Intercom")
			return
		}

		// Create integration record with the access token stored in config.
		intg := domain.Integration{
			ID:        uuid.New(),
			ProjectID: projectID,
			Provider:  "intercom",
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

// IntercomDisconnect handles DELETE /api/v1/projects/{projectId}/intercom/{integrationId}.
// Removes the native Intercom integration.
func IntercomDisconnect(d *Deps) http.HandlerFunc {
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

// TriggerIntercomSync handles POST /api/v1/projects/{projectId}/intercom/{integrationId}/sync.
// Enqueues an IntercomSyncJob for the given integration.
func TriggerIntercomSync(d *Deps) http.HandlerFunc {
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
		runID, taskIDs, err := jobs.CreateIntercomSyncPipeline(r.Context(), d.Store, projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create pipeline")
			return
		}

		// Enqueue the sync job.
		_, err = d.River.Insert(r.Context(), jobs.IntercomSyncJobArgs{
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

// IntercomWebhook handles POST /api/v1/webhooks/intercom.
// Receives real-time conversation events from Intercom.
func IntercomWebhook(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "failed to read body")
			return
		}

		// Verify webhook signature if secret is configured.
		if d.Config.IntercomWebhookSecret != "" {
			sig := r.Header.Get("X-Hub-Signature")
			if !intercom.VerifyWebhook(body, sig, d.Config.IntercomWebhookSecret) {
				respondErr(w, r, http.StatusUnauthorized, "invalid webhook signature")
				return
			}
		}

		var event struct {
			Topic string `json:"topic"`
			Data  struct {
				Item struct {
					ID        string `json:"id"`
					Type      string `json:"type"`
					CreatedAt int64  `json:"created_at"`
					Source    struct {
						Body string `json:"body"`
					} `json:"source"`
				} `json:"item"`
			} `json:"data"`
			AppID string `json:"app_id"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid webhook payload")
			return
		}

		// We only process new conversations.
		if event.Topic != "conversation.created" && event.Topic != "conversation.user.created" {
			w.WriteHeader(http.StatusOK)
			return
		}

		slog.Info("intercom webhook: received conversation event",
			"topic", event.Topic,
			"conversation_id", event.Data.Item.ID,
		)

		// Find all active intercom integrations and insert the signal for each.
		integrations, err := d.Store.ListActiveIntegrationsInternal(r.Context(), "intercom")
		if err != nil {
			slog.Error("intercom webhook: list integrations", "error", err)
			w.WriteHeader(http.StatusOK) // ACK to Intercom even on error
			return
		}

		for _, intg := range integrations {
			conv := intercom.Conversation{
				ID:        event.Data.Item.ID,
				CreatedAt: event.Data.Item.CreatedAt,
				Source: &intercom.ConversationSource{
					Body: event.Data.Item.Source.Body,
				},
			}
			sig := intercom.ConversationToSignal(conv, intg.ProjectID)
			if _, insertErr := d.Store.InsertSignal(r.Context(), sig); insertErr != nil {
				slog.Error("intercom webhook: insert signal",
					"conversation_id", event.Data.Item.ID,
					"project_id", intg.ProjectID,
					"error", insertErr,
				)
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}
}
