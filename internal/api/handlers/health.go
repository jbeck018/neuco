package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/render"
)

// Healthz handles GET /healthz (liveness probe).
// Always returns 200 if the process is running. No auth required.
func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, map[string]string{"status": "ok"})
	}
}

// Readyz handles GET /readyz (readiness probe).
// Checks database connectivity with a 2s timeout.
// Returns 200 if healthy, 503 if degraded. No auth required.
func Readyz(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := d.DB.Stat()
		poolStats := map[string]int64{
			"max_conns":      int64(stats.MaxConns()),
			"acquired_conns": int64(stats.AcquiredConns()),
			"idle_conns":     int64(stats.IdleConns()),
			"total_conns":    int64(stats.TotalConns()),
		}

		dbCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := d.DB.Ping(dbCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			render.JSON(w, r, map[string]any{
				"status":     "unavailable",
				"database":   "error",
				"pool_stats": poolStats,
			})
			return
		}

		render.JSON(w, r, map[string]any{
			"status":     "ok",
			"database":   "ok",
			"pool_stats": poolStats,
		})
	}
}
