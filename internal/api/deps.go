// Package api contains the HTTP router, middleware, and handler dependencies
// for the Neuco backend API.
package api

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neuco-ai/neuco/internal/ai"
	"github.com/neuco-ai/neuco/internal/api/handlers"
	"github.com/neuco-ai/neuco/internal/codegen"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/jobs"
	"github.com/neuco-ai/neuco/internal/store"
	"github.com/riverqueue/river"
)

// Deps bundles all shared dependencies injected into HTTP handlers.
// It is constructed once at startup and passed through to every handler.
// This is the canonical definition; handlers.Deps is a copy for internal use.
type Deps = handlers.Deps

// NewDeps constructs a Deps from the application's core services.
func NewDeps(s *store.Store, riverClient *river.Client[pgx.Tx], jobCtx *jobs.JobContext, cfg *config.Config, db *pgxpool.Pool, providerRegistry *codegen.ProviderRegistry) *Deps {
	llm := ai.NewLLMClient(cfg.AnthropicAPIKey, cfg.OpenAIAPIKey)
	queryEngine := ai.NewSignalQueryEngine(llm, s)

	return &Deps{
		Store:            s,
		River:            riverClient,
		JobCtx:           jobCtx,
		Config:           cfg,
		DB:               db,
		QueryEngine:      queryEngine,
		ProviderRegistry: providerRegistry,
	}
}
