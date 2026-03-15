// Package handlers contains all HTTP handler functions for the Neuco API.
// Handlers are grouped by domain concept, each in their own file.
package handlers

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neuco-ai/neuco/internal/ai"
	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/jobs"
	"github.com/neuco-ai/neuco/internal/store"
	"github.com/riverqueue/river"
)

// Deps bundles all shared dependencies injected into HTTP handlers.
type Deps struct {
	Store            *store.Store
	River            *river.Client[pgx.Tx]
	JobCtx           *jobs.JobContext
	Config           *config.Config
	DB               *pgxpool.Pool
	QueryEngine      *ai.SignalQueryEngine
	ProviderRegistry *codegen.ProviderRegistry
}
