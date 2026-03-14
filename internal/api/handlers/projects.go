package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/domain"
)

// createProjectRequest is the request body for POST /api/v1/orgs/{orgId}/projects.
type createProjectRequest struct {
	Name       string                  `json:"name"`
	GitHubRepo string                  `json:"github_repo"`
	Framework  domain.ProjectFramework `json:"framework"`
	Styling    domain.ProjectStyling   `json:"styling"`
}

// updateProjectRequest is the request body for PATCH /api/v1/projects/{projectId}.
type updateProjectRequest struct {
	Name       *string                  `json:"name,omitempty"`
	GitHubRepo *string                  `json:"github_repo,omitempty"`
	Framework  *domain.ProjectFramework `json:"framework,omitempty"`
	Styling    *domain.ProjectStyling   `json:"styling,omitempty"`
}

// ListOrgProjects handles GET /api/v1/orgs/{orgId}/projects.
func ListOrgProjects(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := mw.ResolvedOrgIDFromCtx(r.Context())

		projects, err := d.Store.ListOrgProjects(r.Context(), orgID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list projects")
			return
		}

		respondOK(w, r, projects)
	}
}

// CreateProject handles POST /api/v1/orgs/{orgId}/projects. Requires member+ role.
func CreateProject(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := mw.ResolvedOrgIDFromCtx(r.Context())

		var req createProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		name := strings.TrimSpace(req.Name)

		validationErr := &ValidationError{}
		if msg := ValidateRequired("name", name); msg != "" {
			validationErr.Add("name", msg)
		}
		if msg := ValidateMaxLength("name", name, MaxNameLen); msg != "" {
			validationErr.Add("name", msg)
		}
		if !isValidFramework(req.Framework) {
			validationErr.Add("framework", "invalid framework")
		}
		if !isValidStyling(req.Styling) {
			validationErr.Add("styling", "invalid styling")
		}
		if validationErr.HasErrors() {
			respondValidation(w, r, validationErr)
			return
		}

		userID := mw.UserIDFromCtx(r.Context())

		project, err := d.Store.CreateProject(r.Context(),
			orgID,
			name,
			req.GitHubRepo,
			req.Framework,
			req.Styling,
			userID,
		)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create project")
			return
		}

		recordAudit(r.Context(), d, orgID, "project.create", "project", project.ID.String(),
			map[string]any{"name": project.Name})
		respondCreated(w, r, project)
	}
}

// GetProjectHandler handles GET /api/v1/projects/{projectId}.
// The project tenant middleware has already verified org membership.
func GetProjectHandler(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		project, err := d.Store.GetProject(r.Context(), orgID, projectID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "project not found")
			return
		}
		respondOK(w, r, project)
	}
}

// UpdateProject handles PATCH /api/v1/projects/{projectId}. Requires admin+ role.
func UpdateProject(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		var req updateProjectRequest
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
		if req.Framework != nil && !isValidFramework(*req.Framework) {
			validationErr.Add("framework", "invalid framework")
		}
		if req.Styling != nil && !isValidStyling(*req.Styling) {
			validationErr.Add("styling", "invalid styling")
		}
		if validationErr.HasErrors() {
			respondValidation(w, r, validationErr)
			return
		}

		updated, err := d.Store.UpdateProject(r.Context(), orgID, projectID, name, req.GitHubRepo, req.Framework, req.Styling)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to update project")
			return
		}

		recordAudit(r.Context(), d, orgID, "project.update", "project", projectID.String(), req)
		respondOK(w, r, updated)
	}
}

// DeleteProject handles DELETE /api/v1/projects/{projectId}. Requires admin+ role.
func DeleteProject(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		if err := d.Store.DeleteProject(r.Context(), orgID, projectID); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to delete project")
			return
		}

		recordAudit(r.Context(), d, orgID, "project.delete", "project", projectID.String(), nil)
		respondNoContent(w, r)
	}
}
