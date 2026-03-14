package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/domain"
)

// nonAlphanumRE matches characters that are not lowercase alphanumeric or hyphens.
var nonAlphanumRE = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify converts a name string into a URL-safe slug.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumRE.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "org-" + uuid.New().String()[:8]
	}
	return s
}

// createOrgRequest is the request body for POST /api/v1/orgs.
type createOrgRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

// updateOrgRequest is the request body for PATCH /api/v1/orgs/{orgId}.
type updateOrgRequest struct {
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

// ListOrgs handles GET /api/v1/orgs.
// Returns all organisations of which the authenticated user is a member.
func ListOrgs(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := mw.UserIDFromCtx(r.Context())
		orgs, err := d.Store.ListUserOrgs(r.Context(), userID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list orgs")
			return
		}
		respondOK(w, r, orgs)
	}
}

// CreateOrg handles POST /api/v1/orgs.
// Creates a new organisation and makes the caller the owner.
func CreateOrg(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createOrgRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		name := strings.TrimSpace(req.Name)
		slug := strings.TrimSpace(req.Slug)

		validationErr := &ValidationError{}
		if msg := ValidateRequired("name", name); msg != "" {
			validationErr.Add("name", msg)
		}
		if msg := ValidateMaxLength("name", name, MaxNameLen); msg != "" {
			validationErr.Add("name", msg)
		}
		if msg := ValidateMaxLength("slug", slug, MaxSlugLen); msg != "" {
			validationErr.Add("slug", msg)
		}
		if validationErr.HasErrors() {
			respondValidation(w, r, validationErr)
			return
		}

		if slug == "" {
			slug = slugify(name)
		}

		userID := mw.UserIDFromCtx(r.Context())

		org, err := d.Store.CreateOrg(r.Context(), name, slug, domain.OrgPlanStarter)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create org")
			return
		}

		if _, err := d.Store.AddMember(r.Context(), org.ID, userID, domain.OrgRoleOwner); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to assign owner")
			return
		}

		recordAudit(r.Context(), d, org.ID, "org.create", "org", org.ID.String(), map[string]any{"name": org.Name})
		respondCreated(w, r, org)
	}
}

// GetOrg handles GET /api/v1/orgs/{orgId}.
func GetOrg(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := mw.ResolvedOrgIDFromCtx(r.Context())

		org, err := d.Store.GetOrgByID(r.Context(), orgID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "org not found")
			return
		}
		respondOK(w, r, org)
	}
}

// UpdateOrg handles PATCH /api/v1/orgs/{orgId}. Requires admin+ role.
// The store's UpdateOrg takes (ctx, id, name *string, plan *OrgPlan).
// Slug updates are not supported by the store directly; we update name only for now.
func UpdateOrg(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := mw.ResolvedOrgIDFromCtx(r.Context())

		var req updateOrgRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		var name *string
		validationErr := &ValidationError{}
		if req.Name != nil {
			trimmed := strings.TrimSpace(*req.Name)
			if msg := ValidateMaxLength("name", trimmed, MaxNameLen); msg != "" {
				validationErr.Add("name", msg)
			}
			name = &trimmed
		}
		if validationErr.HasErrors() {
			respondValidation(w, r, validationErr)
			return
		}

		updated, err := d.Store.UpdateOrg(r.Context(), orgID, name, nil)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to update org")
			return
		}

		recordAudit(r.Context(), d, orgID, "org.update", "org", orgID.String(), req)
		respondOK(w, r, updated)
	}
}
