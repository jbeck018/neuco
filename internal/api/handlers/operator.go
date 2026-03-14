package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var processStartTime = time.Now()

// orgWithStats bundles an org with member count and usage for the operator view.
type orgWithStats struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	Plan         string    `json:"plan"`
	MemberCount  int       `json:"member_count"`
	ProjectCount int       `json:"project_count"`
	SignalCount  int       `json:"signal_count"`
	CreatedAt    time.Time `json:"created_at"`
}

// healthResponse is the system health payload.
type healthResponse struct {
	Status    string            `json:"status"`
	Checks    map[string]string `json:"checks"`
	Timestamp time.Time         `json:"timestamp"`
}

// OperatorListOrgs handles GET /operator/orgs.
// Returns all orgs with member counts and project counts.
func OperatorListOrgs(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgs, err := d.Store.ListAllOrgs(r.Context())
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list orgs")
			return
		}

		result := make([]orgWithStats, 0, len(orgs))
		for _, org := range orgs {
			members, _ := d.Store.ListOrgMembers(r.Context(), org.ID)
			projects, _ := d.Store.ListOrgProjects(r.Context(), org.ID)
			stats := &orgWithStats{
				ID:           org.ID,
				Name:         org.Name,
				Slug:         org.Slug,
				Plan:         string(org.Plan),
				MemberCount:  len(members),
				ProjectCount: len(projects),
				CreatedAt:    org.CreatedAt,
			}
			for _, p := range projects {
				ps, err := d.Store.GetProjectStats(r.Context(), p.ID)
				if err == nil {
					stats.SignalCount += ps.SignalCount
				}
			}
			result = append(result, *stats)
		}

		respondOK(w, r, result)
	}
}

// OperatorGetOrg handles GET /operator/orgs/{orgId}.
// Returns org details with all projects.
func OperatorGetOrg(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID, err := uuid.Parse(chi.URLParam(r, "orgId"))
		if err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid org_id")
			return
		}

		org, err := d.Store.GetOrgByID(r.Context(), orgID)
		if err != nil {
			respondErr(w, r, http.StatusNotFound, "org not found")
			return
		}

		members, _ := d.Store.ListOrgMembers(r.Context(), orgID)
		projects, _ := d.Store.ListOrgProjects(r.Context(), orgID)

		respondOK(w, r, map[string]any{
			"org":      org,
			"members":  members,
			"projects": projects,
		})
	}
}

// OperatorListUsers handles GET /operator/users.
func OperatorListUsers(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := d.Store.ListAllUsers(r.Context())
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list users")
			return
		}
		respondOK(w, r, users)
	}
}

// OperatorHealth handles GET /operator/health.
// Checks database connectivity and returns a health status payload.
func OperatorHealth(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := make(map[string]string)
		overall := "ok"

		// Database connectivity check.
		dbCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.Ping(dbCtx); err != nil {
			checks["database"] = "error: " + err.Error()
			overall = "degraded"
		} else {
			checks["database"] = "ok"
		}

		// River queue depth check via the store.
		queueDepths, err := d.Store.GetQueueDepths(r.Context())
		if err != nil {
			checks["queue"] = "error: " + err.Error()
			overall = "degraded"
		} else {
			checks["queue"] = queueDepths
		}

		resp := healthResponse{
			Status:    overall,
			Checks:    checks,
			Timestamp: time.Now().UTC(),
		}

		statusCode := http.StatusOK
		if overall != "ok" {
			statusCode = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		respondOK(w, r, resp)
	}
}

// OperatorMetrics handles GET /operator/metrics.
// Returns basic runtime and process metrics for operators.
func OperatorMetrics() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		respondOK(w, r, map[string]any{
			"runtime": map[string]any{
				"goroutines": runtime.NumGoroutine(),
				"heap_alloc": m.HeapAlloc,
				"sys_memory": m.Sys,
			},
			"process": map[string]any{
				"uptime_seconds": int64(time.Since(processStartTime).Seconds()),
			},
		})
	}
}

// OperatorListFlags handles GET /operator/flags.
// Returns all feature flags.
func OperatorListFlags(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flags, err := d.Store.ListFlags(r.Context())
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to list flags")
			return
		}
		respondOK(w, r, flags)
	}
}

// OperatorUpdateFlag handles PATCH /operator/flags/{key}.
// Updates the enabled status of a feature flag.
func OperatorUpdateFlag(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := chi.URLParam(r, "key")
		if key == "" {
			respondErr(w, r, http.StatusBadRequest, "missing flag key")
			return
		}

		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, r, http.StatusBadRequest, "invalid request body")
			return
		}

		// Operator routes use a static token — pass nil for updated_by.
		if err := d.Store.SetFlag(r.Context(), key, req.Enabled, nil); err != nil {
			respondErr(w, r, http.StatusNotFound, "flag not found")
			return
		}

		flag, err := d.Store.GetFlag(r.Context(), key)
		if err != nil {
			respondErr(w, r, http.StatusInternalServerError, "failed to fetch updated flag")
			return
		}

		respondOK(w, r, flag)
	}
}
