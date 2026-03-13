package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/jobs"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/store"
	"github.com/riverqueue/river"
)

// enqueueCodegenResponse is returned when a codegen job is enqueued.
type enqueueCodegenResponse struct {
	GenerationID  string `json:"generation_id"`
	PipelineRunID string `json:"pipeline_run_id"`
}

// generationProgressEvent is the SSE payload sent on each tick.
type generationProgressEvent struct {
	Generation *domain.Generation `json:"generation"`
	Run        *domain.PipelineRun `json:"run,omitempty"`
}

// generationPage is the paginated list response.
type generationPage struct {
	Generations []domain.Generation `json:"generations"`
	Total       int                 `json:"total"`
}

// EnqueueCodegen handles POST /api/v1/projects/{projectId}/candidates/{cId}/generate.
func EnqueueCodegen(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		candidateID, err := uuid.Parse(chi.URLParam(r, "cId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid candidate_id")
			return
		}

		if _, err := d.Store.GetCandidate(r.Context(), projectID, candidateID); err != nil {
			respondErr(w, r, http.StatusNotFound, "candidate not found")
			return
		}

		spec, err := d.Store.GetSpecByCandidate(r.Context(), projectID, candidateID)
		if err != nil {
			respondErr(w, r, http.StatusConflict, "no spec found for candidate — generate a spec first")
			return
		}

		// CreateCodegenPipeline creates the run and all tasks, returns (runID, taskIDs, err).
		runID, taskIDs, err := jobs.CreateCodegenPipeline(r.Context(), d.Store, projectID, spec.ID)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create codegen pipeline")
			return
		}

		generationID := uuid.New()
		gen := domain.Generation{
			ID:            generationID,
			ProjectID:     projectID,
			SpecID:        spec.ID,
			PipelineRunID: runID,
			Status:        domain.GenerationStatusPending,
		}
		if err := d.Store.CreateGeneration(r.Context(), &gen); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to create generation record")
			return
		}

		// Enqueue the first codegen task: fetch_spec.
		var firstTaskID uuid.UUID
		if len(taskIDs) > 0 {
			firstTaskID = taskIDs[0]
		}

		_, err = d.River.Insert(r.Context(), jobs.FetchSpecJobArgs{
			SpecID:    spec.ID,
			ProjectID: projectID,
			RunID:     runID,
			TaskID:    firstTaskID,
		}, &river.InsertOpts{})
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to enqueue codegen job")
			return
		}

		recordAudit(r.Context(), d, orgID, "generation.create", "generation", generationID.String(),
			map[string]any{"candidate_id": candidateID.String(), "run_id": runID.String()})
		respondCreated(w, r, enqueueCodegenResponse{
			GenerationID:  generationID.String(),
			PipelineRunID: runID.String(),
		})
	}
}

// ListGenerations handles GET /api/v1/projects/{projectId}/generations.
func ListGenerations(d *Deps) http.HandlerFunc {
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

		generations, total, err := d.Store.ListProjectGenerations(r.Context(), projectID, store.Page(limit, offset))
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list generations")
			return
		}

		respondOK(w, r, generationPage{Generations: generations, Total: total})
	}
}

// GetGeneration handles GET /api/v1/projects/{projectId}/generations/{gId}.
func GetGeneration(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		gID, err := uuid.Parse(chi.URLParam(r, "gId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid generation_id")
			return
		}

		gen, err := d.Store.GetGeneration(r.Context(), gID)
		if err != nil || gen.ProjectID != projectID {
			respondErr(w, r, http.StatusNotFound, "generation not found")
			return
		}

		respondOK(w, r, gen)
	}
}

// StreamGenerationProgress handles GET /api/v1/projects/{projectId}/generations/{gId}/stream.
// Streams pipeline task state updates as Server-Sent Events, polling every 750 ms.
func StreamGenerationProgress(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		gID, err := uuid.Parse(chi.URLParam(r, "gId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid generation_id")
			return
		}

		gen, err := d.Store.GetGeneration(r.Context(), gID)
		if err != nil || gen.ProjectID != projectID {
			respondErr(w, r, http.StatusNotFound, "generation not found")
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		writeSSE := func(event string, payload any) {
			data, jerr := json.Marshal(payload)
			if jerr != nil {
				data = []byte(`{}`)
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(data))
			flusher.Flush()
		}

		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				currentGen, ferr := d.Store.GetGeneration(r.Context(), gID)
				if ferr != nil {
					writeSSE("error", map[string]string{"error": "failed to fetch generation"})
					return
				}

				event := generationProgressEvent{Generation: currentGen}

				// Fetch the pipeline run (includes embedded tasks).
				run, ferr := d.Store.GetPipelineRun(r.Context(), currentGen.PipelineRunID)
				if ferr == nil {
					event.Run = run
				}

				writeSSE("progress", event)

				if currentGen.Status == domain.GenerationStatusCompleted ||
					currentGen.Status == domain.GenerationStatusFailed {
					writeSSE("done", map[string]string{"status": string(currentGen.Status)})
					return
				}
			}
		}
	}
}
