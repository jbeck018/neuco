package handlers

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/ai"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/jobs"
	"github.com/neuco-ai/neuco/internal/store"
	"github.com/riverqueue/river"
)

// signalUploadResponse is the response body for POST …/signals/upload.
type signalUploadResponse struct {
	Inserted      int    `json:"inserted"`
	Deduplicated  int    `json:"deduplicated"`
	PipelineRunID string `json:"pipeline_run_id,omitempty"`
}

// UploadSignals handles POST /api/v1/projects/{projectId}/signals/upload.
// Accepts multipart/form-data with a "file" field (CSV or plain text).
func UploadSignals(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		const maxUploadSize = 32 << 20 // 32 MiB
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

		if err := r.ParseMultipartForm(maxUploadSize); err != nil {
			respondErr(w, r, http.StatusBadRequest, "failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "file field is required")
			return
		}
		defer func() { _ = file.Close() }()

		contentType := header.Header.Get("Content-Type")
		filename := header.Filename
		now := time.Now().UTC()

		isCSV := strings.HasSuffix(strings.ToLower(filename), ".csv") ||
			strings.Contains(contentType, "csv")

		var signals []domain.Signal
		if isCSV {
			signals, err = parseCSVSignals(file, projectID, now)
		} else {
			signals, err = parsePlainTextSignals(file, projectID, now)
		}
		if err != nil {
			respondErr(w, r, http.StatusUnprocessableEntity, "failed to parse file: "+err.Error())
			return
		}

		if len(signals) == 0 {
			respondErr(w, r, http.StatusBadRequest, "no signals found in file")
			return
		}

		// Insert signals and collect their IDs, skipping exact duplicates.
		var signalIDs []uuid.UUID
		dedupCount := 0
		for i := range signals {
			sig, err := d.Store.InsertSignal(r.Context(), signals[i])
			if err != nil {
				if errors.Is(err, store.ErrDuplicateSignal) {
					dedupCount++
				}
				continue
			}
			signalIDs = append(signalIDs, sig.ID)
		}

		inserted := len(signalIDs)

		// Create an ingest pipeline for batch embedding.
		var pipelineRunID string
		if inserted > 0 {
			runID, taskIDs, perr := jobs.CreateIngestPipeline(r.Context(), d.Store, projectID)
			if perr == nil && len(taskIDs) >= 2 {
				// Task 0 = ingest, Task 1 = embed
				_, _ = d.River.Insert(r.Context(), jobs.EmbedJobArgs{
					ProjectID: projectID,
					SignalIDs: signalIDs,
					RunID:     runID,
					TaskID:    taskIDs[1], // embed task
				}, &river.InsertOpts{})
				pipelineRunID = runID.String()
			}
		}

		orgID := mw.OrgIDFromCtx(r.Context())
		recordAudit(r.Context(), d, orgID, "signal.upload", "signal", projectID.String(),
			map[string]any{"inserted": inserted, "filename": filename})

		respondCreated(w, r, signalUploadResponse{Inserted: inserted, Deduplicated: dedupCount, PipelineRunID: pipelineRunID})
	}
}

func parseCSVSignals(r io.Reader, projectID uuid.UUID, now time.Time) ([]domain.Signal, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}

	colIdx := make(map[string]int)
	for i, h := range headers {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	contentCol := colIdx["content"]
	sourceCol, hasSource := colIdx["source"]
	typeCol, hasType := colIdx["type"]
	sourceRefCol, hasSourceRef := colIdx["source_ref"]

	var signals []domain.Signal
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if len(row) == 0 {
			continue
		}

		content := ""
		if contentCol < len(row) {
			content = strings.TrimSpace(row[contentCol])
		}
		if content == "" {
			continue
		}

		src := domain.SignalSourceCSV
		if hasSource && sourceCol < len(row) && row[sourceCol] != "" {
			src = domain.SignalSource(strings.TrimSpace(row[sourceCol]))
		}

		typ := domain.SignalTypeFeatureRequest
		if hasType && typeCol < len(row) && row[typeCol] != "" {
			typ = domain.SignalType(strings.TrimSpace(row[typeCol]))
		}

		sourceRef := ""
		if hasSourceRef && sourceRefCol < len(row) {
			sourceRef = strings.TrimSpace(row[sourceRefCol])
		}

		signals = append(signals, domain.Signal{
			ID:         uuid.New(),
			ProjectID:  projectID,
			Source:     src,
			SourceRef:  sourceRef,
			Type:       typ,
			Content:    content,
			Metadata:   json.RawMessage("{}"),
			OccurredAt: now,
		})
	}

	return signals, nil
}

func parsePlainTextSignals(r io.Reader, projectID uuid.UUID, now time.Time) ([]domain.Signal, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	paragraphs := strings.Split(string(data), "\n\n")

	var signals []domain.Signal
	for _, p := range paragraphs {
		content := strings.TrimSpace(p)
		if content == "" {
			continue
		}
		signals = append(signals, domain.Signal{
			ID:         uuid.New(),
			ProjectID:  projectID,
			Source:     domain.SignalSourceManual,
			Type:       domain.SignalTypeFeatureRequest,
			Content:    content,
			Metadata:   json.RawMessage("{}"),
			OccurredAt: now,
		})
	}

	return signals, nil
}

// ListSignals handles GET /api/v1/projects/{projectId}/signals.
func ListSignals(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		limit := 50
		offset := 0
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if n, err := strconv.Atoi(lStr); err == nil && n > 0 {
				limit = n
			}
		}
		limit = clampPagination(limit)
		if oStr := r.URL.Query().Get("offset"); oStr != "" {
			if n, err := strconv.Atoi(oStr); err == nil && n >= 0 {
				offset = n
			}
		}

		filters := store.SignalFilters{}
		if src := r.URL.Query().Get("source"); src != "" {
			filters.Sources = []string{src}
		}
		if typ := r.URL.Query().Get("type"); typ != "" {
			filters.Types = []string{typ}
		}
		if from := r.URL.Query().Get("from"); from != "" {
			if t, err := time.Parse(time.RFC3339, from); err == nil {
				filters.From = &t
			}
		}
		if to := r.URL.Query().Get("to"); to != "" {
			if t, err := time.Parse(time.RFC3339, to); err == nil {
				filters.To = &t
			}
		}
		if r.URL.Query().Get("exclude_duplicates") == "true" {
			filters.ExcludeDuplicates = true
		}

		page, err := d.Store.ListProjectSignals(r.Context(), projectID, filters, store.Page(limit, offset))
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list signals")
			return
		}

		respondOK(w, r, map[string]any{
			"signals": page.Signals,
			"total":   page.Total,
		})
	}
}

// DeleteSignal handles DELETE /api/v1/projects/{projectId}/signals/{signalId}.
func DeleteSignal(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())
		orgID := mw.OrgIDFromCtx(r.Context())

		signalID, err := uuid.Parse(chi.URLParam(r, "signalId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid signal_id")
			return
		}

		if _, err := d.Store.GetSignal(r.Context(), projectID, signalID); err != nil {
			respondErr(w, r, http.StatusNotFound, "signal not found")
			return
		}

		if err := d.Store.DeleteSignal(r.Context(), projectID, signalID); err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to delete signal")
			return
		}

		recordAudit(r.Context(), d, orgID, "signal.delete", "signal", signalID.String(), nil)
		respondNoContent(w, r)
	}
}

// signalQueryRequest is the JSON body for POST …/signals/query.
type signalQueryRequest struct {
	Question string               `json:"question"`
	Filters  signalQueryFilterDTO `json:"filters"`
}

// signalQueryFilterDTO mirrors ai.SignalQueryFilters for JSON decoding.
type signalQueryFilterDTO struct {
	Sources []string   `json:"sources"`
	Types   []string   `json:"types"`
	From    *time.Time `json:"from"`
	To      *time.Time `json:"to"`
	Limit   int        `json:"limit"`
}

// QuerySignals handles POST /api/v1/projects/{projectId}/signals/query.
//
// Accepts:
//
//	{
//	    "question": "What do users wish the export feature could do?",
//	    "filters": { "sources": ["gong"], "limit": 10 }
//	}
//
// Returns an array of signals ranked by semantic similarity together with
// their cosine distance score.
func QuerySignals(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mw.ProjectIDFromCtx(r.Context())

		var body signalQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		if strings.TrimSpace(body.Question) == "" {
			respondErr(w, r, http.StatusBadRequest, "question is required")
			return
		}
		if msg := validateStringLen("question", body.Question, MaxDescriptionLen); msg != "" {
			respondErr(w, r, http.StatusBadRequest, msg)
			return
		}

		if d.QueryEngine == nil {
			respondErr(w, r, http.StatusServiceUnavailable, "query engine not configured")
			return
		}

		filters := ai.SignalQueryFilters{
			Sources: body.Filters.Sources,
			Types:   body.Filters.Types,
			From:    body.Filters.From,
			To:      body.Filters.To,
			Limit:   body.Filters.Limit,
		}

		results, err := d.QueryEngine.Query(r.Context(), projectID, body.Question, filters)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}

		respondOK(w, r, map[string]any{
			"question": body.Question,
			"results":  results,
			"total":    len(results),
		})
	}
}
