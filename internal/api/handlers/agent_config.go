package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/store"
)

type upsertAgentConfigRequest struct {
	Provider      string            `json:"provider"`
	APIKey        *string           `json:"api_key,omitempty"`
	ModelOverride *string           `json:"model_override,omitempty"`
	IsDefault     bool              `json:"is_default"`
	ExtraConfig   map[string]string `json:"extra_config,omitempty"`
}

type validateAgentConfigRequest struct {
	Provider      string            `json:"provider"`
	APIKey        *string           `json:"api_key,omitempty"`
	ModelOverride *string           `json:"model_override,omitempty"`
	ExtraConfig   map[string]string `json:"extra_config,omitempty"`
}

type agentConfigResponse struct {
	ID            uuid.UUID         `json:"id"`
	OrgID         uuid.UUID         `json:"org_id"`
	ProjectID     *uuid.UUID        `json:"project_id,omitempty"`
	Provider      string            `json:"provider"`
	ModelOverride *string           `json:"model_override,omitempty"`
	ExtraConfig   map[string]string `json:"extra_config,omitempty"`
	HasAPIKey     bool              `json:"has_api_key"`
	IsDefault     bool              `json:"is_default"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type providerMetadataResponse struct {
	Name                string `json:"name"`
	DisplayName         string `json:"display_name"`
	InstallInstructions string `json:"install_instructions"`
	Installed           bool   `json:"installed"`
}

// GetAgentConfig handles GET /api/v1/projects/{projectId}/agent-config.
// Returns the effective config (project override, then org default), with API key redacted.
func GetAgentConfig(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		cfg, err := d.Store.GetEffectiveConfig(r.Context(), orgID, projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to get agent config")
			return
		}
		if cfg == nil {
			respondErr(w, r, http.StatusNotFound, "agent config not found")
			return
		}

		respondOK(w, r, toAgentConfigResponse(*cfg))
	}
}

// UpsertAgentConfig handles PUT /api/v1/projects/{projectId}/agent-config.
func UpsertAgentConfig(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		var req upsertAgentConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		providerName := strings.TrimSpace(req.Provider)
		if providerName == "" {
			respondErr(w, r, http.StatusBadRequest, "provider is required")
			return
		}

		if _, ok := d.ProviderRegistry.Get(providerName); !ok {
			respondErr(w, r, http.StatusBadRequest, "invalid provider")
			return
		}

		existing, err := findScopedAgentConfig(r.Context(), d.Store, orgID, projectID, providerName)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to load existing agent config")
			return
		}

		var encryptedAPIKey []byte
		switch {
		case req.APIKey == nil:
			if existing != nil {
				encryptedAPIKey = existing.EncryptedAPIKey
			}
		default:
			apiKey := strings.TrimSpace(*req.APIKey)
			if apiKey != "" {
				key, err := codegen.DeriveKey(d.Config.EncryptionKey)
				if err != nil {
					respondErr(w, r, http.StatusInternalServerError, "failed to derive encryption key")
					return
				}
				encryptedAPIKey, err = codegen.Encrypt([]byte(apiKey), key)
				if err != nil {
					respondErr(w, r, http.StatusInternalServerError, "failed to encrypt api key")
					return
				}
			}
		}

		modelOverride := req.ModelOverride
		if modelOverride != nil {
			trimmed := strings.TrimSpace(*modelOverride)
			modelOverride = &trimmed
		} else if existing != nil {
			modelOverride = existing.ModelOverride
		}

		extraConfig := req.ExtraConfig
		if extraConfig == nil {
			extraConfig = map[string]string{}
			if existing != nil {
				extraConfig = decodeExtraConfig(existing.ExtraConfig)
			}
		}

		extraJSON, err := json.Marshal(extraConfig)
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid extra_config")
			return
		}

		projectIDPtr := &projectID
		cfg := store.AgentConfigRow{
			OrgID:           orgID,
			ProjectID:       projectIDPtr,
			Provider:        providerName,
			EncryptedAPIKey: encryptedAPIKey,
			ModelOverride:   modelOverride,
			ExtraConfig:     extraJSON,
			IsDefault:       req.IsDefault,
		}

		if existing != nil {
			cfg.ID = existing.ID
		}

		if err := d.Store.SetAgentConfig(r.Context(), cfg); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to upsert agent config")
			return
		}

		updated, err := findScopedAgentConfig(r.Context(), d.Store, orgID, projectID, providerName)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to fetch updated agent config")
			return
		}
		if updated == nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to fetch updated agent config")
			return
		}

		respondOK(w, r, toAgentConfigResponse(*updated))
	}
}

// DeleteAgentConfig handles DELETE /api/v1/projects/{projectId}/agent-config?provider=<name>.
func DeleteAgentConfig(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
		if providerName == "" {
			respondErr(w, r, http.StatusBadRequest, "provider is required")
			return
		}

		projectIDPtr := &projectID
		if err := d.Store.DeleteAgentConfig(r.Context(), orgID, projectIDPtr, providerName); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to delete agent config")
			return
		}

		respondNoContent(w, r)
	}
}

// ValidateAgentConfig handles POST /api/v1/projects/{projectId}/agent-config/validate.
func ValidateAgentConfig(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req validateAgentConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		providerName := strings.TrimSpace(req.Provider)
		if providerName == "" {
			respondErr(w, r, http.StatusBadRequest, "provider is required")
			return
		}

		provider, ok := d.ProviderRegistry.Get(providerName)
		if !ok {
			respondErr(w, r, http.StatusBadRequest, "invalid provider")
			return
		}

		cfg := codegen.AgentConfig{
			Provider:    providerName,
			ExtraConfig: req.ExtraConfig,
		}

		if req.ModelOverride != nil {
			cfg.ModelOverride = strings.TrimSpace(*req.ModelOverride)
		}
		if req.APIKey != nil {
			apiKey := strings.TrimSpace(*req.APIKey)
			if apiKey != "" {
				cfg.EncryptedAPIKey = []byte(apiKey)
			}
		}

		if err := provider.ValidateConfig(r.Context(), cfg); err != nil {
			respondOK(w, r, map[string]any{"valid": false, "error": err.Error()})
			return
		}

		respondOK(w, r, map[string]any{"valid": true, "error": ""})
	}
}

// ListAgentProviders handles GET /agent-providers.
func ListAgentProviders(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		names := d.ProviderRegistry.List()
		out := make([]providerMetadataResponse, 0, len(names))
		pathEnv := os.Getenv("PATH")

		for _, name := range names {
			provider, ok := d.ProviderRegistry.Get(name)
			if !ok {
				continue
			}

			out = append(out, providerMetadataResponse{
				Name:                provider.Name(),
				DisplayName:         provider.DisplayName(),
				InstallInstructions: provider.InstallInstructions(),
				Installed:           provider.DetectInstalled(pathEnv),
			})
		}

		respondOK(w, r, out)
	}
}

func toAgentConfigResponse(cfg store.AgentConfigRow) agentConfigResponse {
	return agentConfigResponse{
		ID:            cfg.ID,
		OrgID:         cfg.OrgID,
		ProjectID:     cfg.ProjectID,
		Provider:      cfg.Provider,
		ModelOverride: cfg.ModelOverride,
		ExtraConfig:   decodeExtraConfig(cfg.ExtraConfig),
		HasAPIKey:     len(cfg.EncryptedAPIKey) > 0,
		IsDefault:     cfg.IsDefault,
		CreatedAt:     cfg.CreatedAt,
		UpdatedAt:     cfg.UpdatedAt,
	}
}

func decodeExtraConfig(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}

	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]string{}
	}
	if out == nil {
		return map[string]string{}
	}
	return out
}

func findScopedAgentConfig(ctx context.Context, s *store.Store, orgID uuid.UUID, projectID uuid.UUID, provider string) (*store.AgentConfigRow, error) {
	rows, err := s.ListOrgAgentConfigs(ctx, orgID)
	if err != nil {
		return nil, err
	}

	for i := range rows {
		row := rows[i]
		if row.Provider != provider {
			continue
		}
		if row.ProjectID == nil {
			continue
		}
		if *row.ProjectID != projectID {
			continue
		}
		return &row, nil
	}

	return nil, nil
}
