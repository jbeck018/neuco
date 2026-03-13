package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// pipelinePage is the paginated list response for pipeline runs.
type pipelinePage struct {
	Runs  []domain.PipelineRun `json:"runs"`
	Total int                  `json:"total"`
}

// ListPipelines handles GET /api/v1/projects/{projectId}/pipelines.
func ListPipelines(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		limit := 20
		offset := 0
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if n, err := strconv.Atoi(lStr); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if oStr := r.URL.Query().Get("offset"); oStr != "" {
			if n, err := strconv.Atoi(oStr); err == nil && n >= 0 {
				offset = n
			}
		}

		runs, total, err := d.Store.ListProjectPipelines(r.Context(), projectID, store.Page(limit, offset))
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list pipelines")
			return
		}

		respondOK(w, r, pipelinePage{Runs: runs, Total: total})
	}
}

// GetPipeline handles GET /api/v1/projects/{projectId}/pipelines/{runId}.
func GetPipeline(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		runID, err := uuid.Parse(chi.URLParam(r, "runId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid run_id")
			return
		}

		run, err := d.Store.GetPipelineRunScoped(r.Context(), projectID, runID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "pipeline run not found")
			return
		}

		respondOK(w, r, run)
	}
}

// RetryPipeline handles POST /api/v1/projects/{projectId}/pipelines/{runId}/retry.
func RetryPipeline(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		runID, err := uuid.Parse(chi.URLParam(r, "runId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid run_id")
			return
		}

		// Verify the run belongs to this project.
		run, err := d.Store.GetPipelineRunScoped(r.Context(), projectID, runID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "pipeline run not found")
			return
		}

		// Re-enqueue failed tasks by resetting their status
		for _, task := range run.Tasks {
			if task.Status == domain.PipelineTaskStatusFailed {
				_ = d.Store.UpdatePipelineTaskStatus(r.Context(), task.ID, domain.PipelineTaskStatusPending, "", 0)
			}
		}

		// Reset run status to running
		_, err = d.Store.UpdatePipelineRunStatus(r.Context(), runID, domain.PipelineRunStatusRunning)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to retry pipeline")
			return
		}

		respondOK(w, r, map[string]string{"status": "retry_enqueued", "run_id": runID.String()})
	}
}

// GetProjectStats handles GET /api/v1/projects/{projectId}/stats.
func GetProjectStats(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		stats, err := d.Store.GetProjectStats(r.Context(), projectID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to get project stats")
			return
		}

		respondOK(w, r, stats)
	}
}
