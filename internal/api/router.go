package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/neuco-ai/neuco/internal/api/handlers"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/domain"
)

// NewRouter constructs the full Chi router with all routes, middleware stacks,
// and handler registrations. It accepts a Deps bundle so handlers can access
// the store, River client, and configuration.
func NewRouter(d *Deps, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	// ─── Global middleware ────────────────────────────────────────────────────
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.RequestID)
	r.Use(mw.SentryContext)
	r.Use(mw.SentryRecovery)
	r.Use(mw.CORS(d.Config.FrontendURL))
	r.Use(mw.RequestLogger(logger))

	// ─── Health check endpoints (public, no auth) ──────────────────────────
	r.Get("/health", handlers.Healthz())
	r.Get("/healthz", handlers.Healthz())
	r.Get("/ready", handlers.Readyz(d))
	r.Get("/readyz", handlers.Readyz(d))

	// ─── API documentation ───────────────────────────────────────────────────
	r.Get("/docs", handlers.DocsUI())
	r.Get("/docs/openapi.yaml", handlers.DocsSpec())

	// ─── Public auth routes ───────────────────────────────────────────────────
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Use(mw.AuthRateLimit())
		r.Use(mw.MaxBodySize(1 << 20)) // 1 MiB for JSON auth payloads
		r.Post("/github/callback", handlers.GitHubCallback(d))
		r.Post("/google/callback", handlers.GoogleCallback(d))
		r.Post("/refresh", handlers.RefreshToken(d))
		r.Post("/logout", handlers.Logout(d))

		// Protected auth routes (require a valid JWT).
		r.Group(func(r chi.Router) {
			r.Use(mw.Authenticate(d.Config.JWTSecret))
			r.Get("/me", handlers.Me(d))
			r.Get("/github/repos", handlers.ListUserRepos(d))
			r.Post("/nango/connect-session", handlers.CreateNangoConnectSession(d))
		})
	})

	// ─── Webhook routes (no JWT, rate-limited) ───────────────────────────────
	r.Route("/api/v1/webhooks", func(r chi.Router) {
		r.Use(mw.WebhookRateLimit())
		r.Use(mw.MaxBodySize(1 << 20)) // 1 MiB for webhook payloads
		r.Post("/{projectId}/{secret}", handlers.Webhook(d))
		r.Post("/stripe", handlers.StripeWebhook(d))
		r.Post("/intercom", handlers.IntercomWebhook(d))
		r.Post("/slack", handlers.SlackWebhook(d))
		r.Post("/linear", handlers.LinearWebhook(d))
		r.Post("/jira", handlers.JiraWebhook(d))
	})

	// ─── Authenticated API routes ─────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(mw.Authenticate(d.Config.JWTSecret))
		r.Use(mw.SetSentryUser)
		r.Use(mw.DefaultRateLimit())
		r.Use(mw.MaxBodySize(1 << 20)) // 1 MiB default for JSON endpoints

		// Onboarding routes (user-scoped, not org-scoped).
		r.Route("/api/v1/onboarding", func(r chi.Router) {
			r.Get("/status", handlers.GetOnboardingStatus(d))
			r.Post("/step", handlers.CompleteOnboardingStep(d))
			r.Post("/skip", handlers.SkipOnboarding(d))
		})

		// Org-level routes.
		r.Route("/api/v1/orgs", func(r chi.Router) {
			r.Get("/", handlers.ListOrgs(d))
			r.Post("/", handlers.CreateOrg(d))

			r.Route("/{orgId}", func(r chi.Router) {
				r.Use(mw.ResolveOrg(d.Store))
				r.Get("/", handlers.GetOrg(d))
				r.With(mw.RequireRole(domain.OrgRoleAdmin)).Patch("/", handlers.UpdateOrg(d))

				// Member management.
				r.Route("/members", func(r chi.Router) {
					r.Get("/", handlers.ListMembers(d))
					r.Put("/me/digest", handlers.UpdateDigestOptOut(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/invite", handlers.InviteMember(d))
					r.With(mw.RequireRole(domain.OrgRoleOwner)).Patch("/{userId}", handlers.UpdateMemberRole(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{userId}", handlers.RemoveMember(d))
				})

				// Projects under an org.
				r.Route("/projects", func(r chi.Router) {
					r.Get("/", handlers.ListOrgProjects(d))
					r.With(mw.RequireRole(domain.OrgRoleMember), mw.CheckProjectLimit(d.Store)).
						Post("/", handlers.CreateProject(d))
				})

				// GitHub App integration.
				// POST /installations — called by the frontend after the user
				//   completes the GitHub App install flow on github.com and GitHub
				//   appends ?installation_id=<n> to the configured callback URL.
				// GET  /repos — lists all repos accessible to the installation.
				r.Route("/github", func(r chi.Router) {
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).
						Post("/installations", handlers.GitHubInstallCallback(d))
					r.Get("/repos", handlers.GitHubListRepos(d))
				})

				// Billing & usage.
				r.Route("/billing", func(r chi.Router) {
					r.Get("/subscription", handlers.GetSubscription(d))
					r.Get("/usage", handlers.GetUsage(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/checkout", handlers.CreateCheckoutSession(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/portal", handlers.CreatePortalSession(d))
				})

				// Analytics dashboard (org-level).
				r.Get("/analytics", handlers.GetOrgAnalytics(d))

				// LLM usage (org-level aggregate).
				r.Get("/llm-usage", handlers.GetOrgLLMUsage(d))

				// Notifications.
				r.Route("/notifications", func(r chi.Router) {
					r.Get("/", handlers.ListNotifications(d))
					r.Get("/unread-count", handlers.UnreadNotificationCount(d))
					r.Patch("/{notificationId}/read", handlers.MarkNotificationRead(d))
					r.Post("/read-all", handlers.MarkAllNotificationsRead(d))
				})

				// Audit log.
				r.Get("/audit-log", handlers.AuditLog(d))
			})
		})

		// Project-scoped routes — tenant middleware verifies project belongs to org.
		r.Route("/api/v1/projects/{projectId}", func(r chi.Router) {
			r.Use(mw.ProjectTenant(d.Store))
			r.Use(mw.RequireActiveSubscription(d.Store))

			r.Get("/", handlers.GetProjectHandler(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Patch("/", handlers.UpdateProject(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/", handlers.DeleteProject(d))

			// Signals.
			r.Route("/signals", func(r chi.Router) {
				r.Get("/", handlers.ListSignals(d))
				r.With(mw.CheckSignalLimit(d.Store)).Post("/upload", handlers.UploadSignals(d))
				r.Post("/query", handlers.QuerySignals(d))
				r.Delete("/{signalId}", handlers.DeleteSignal(d))
			})

			// Candidates.
			r.Route("/candidates", func(r chi.Router) {
				r.Get("/", handlers.ListCandidates(d))
				r.Post("/refresh", handlers.RefreshCandidates(d))
				r.Patch("/{cId}", handlers.UpdateCandidateStatus(d))

				// Specs (nested under candidates).
				r.Route("/{cId}/spec", func(r chi.Router) {
					r.Get("/", handlers.GetSpec(d))
					r.Patch("/", handlers.UpdateSpec(d))
					r.Post("/generate", handlers.GenerateSpec(d))
				})

				// Codegen (nested under candidates).
				r.With(mw.GenerationRateLimit(), mw.CheckPRLimit(d.Store)).
					Post("/{cId}/generate", handlers.EnqueueCodegen(d))
			})

			// Generations.
			r.Route("/generations", func(r chi.Router) {
				r.Get("/", handlers.ListGenerations(d))
				r.Get("/{gId}", handlers.GetGeneration(d))
				r.Get("/{gId}/stream", handlers.StreamGenerationProgress(d))
			})

			// Pipeline runs.
			r.Route("/pipelines", func(r chi.Router) {
				r.Get("/", handlers.ListPipelines(d))
				r.Get("/{runId}", handlers.GetPipeline(d))
				r.Post("/{runId}/retry", handlers.RetryPipeline(d))
			})

			// Project stats.
			r.Get("/stats", handlers.GetProjectStats(d))

			// LLM usage & cost tracking.
			r.Route("/llm-usage", func(r chi.Router) {
				r.Get("/", handlers.GetProjectLLMUsage(d))
				r.Get("/calls", handlers.ListProjectLLMCalls(d))
			})
			r.Get("/pipelines/{runId}/llm-usage", handlers.GetPipelineLLMUsage(d))

			// Project context (cross-session memory).
			r.Route("/contexts", func(r chi.Router) {
				r.Get("/", handlers.ListProjectContexts(d))
				r.Post("/", handlers.CreateProjectContext(d))
				r.Post("/search", handlers.SearchProjectContexts(d))
				r.Route("/{contextId}", func(r chi.Router) {
					r.Get("/", handlers.GetProjectContext(d))
					r.Patch("/", handlers.UpdateProjectContext(d))
					r.Delete("/", handlers.DeleteProjectContext(d))
				})
			})

			// Copilot notes.
			r.Route("/copilot/notes", func(r chi.Router) {
				r.Get("/", handlers.ListCopilotNotes(d))
				r.Patch("/{noteId}", handlers.DismissCopilotNote(d))
			})

			// Integrations.
			r.Route("/integrations", func(r chi.Router) {
				r.Get("/", handlers.ListIntegrations(d))
				r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/", handlers.CreateIntegration(d))
				r.Get("/{integrationId}", handlers.GetIntegration(d))
				r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{integrationId}", handlers.DeleteIntegration(d))
			})

			// Native Intercom integration (OAuth + API).
		r.Route("/intercom", func(r chi.Router) {
			r.Get("/authorize", handlers.IntercomAuthorizeURL(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/callback", handlers.IntercomCallback(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{integrationId}", handlers.IntercomDisconnect(d))
			r.Post("/{integrationId}/sync", handlers.TriggerIntercomSync(d))
		})

		// Native Slack integration (OAuth + API).
		r.Route("/slack", func(r chi.Router) {
			r.Get("/authorize", handlers.SlackAuthorizeURL(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/callback", handlers.SlackCallback(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{integrationId}", handlers.SlackDisconnect(d))
			r.Post("/{integrationId}/sync", handlers.TriggerSlackSync(d))
		})

		// Native Linear integration (OAuth + API).
		r.Route("/linear", func(r chi.Router) {
			r.Get("/authorize", handlers.LinearAuthorizeURL(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/callback", handlers.LinearCallback(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{integrationId}", handlers.LinearDisconnect(d))
			r.Post("/{integrationId}/sync", handlers.TriggerLinearSync(d))
		})

		// Native Jira integration (OAuth 2.0 3LO + REST API).
		r.Route("/jira", func(r chi.Router) {
			r.Get("/authorize", handlers.JiraAuthorizeURL(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/callback", handlers.JiraCallback(d))
			r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{integrationId}", handlers.JiraDisconnect(d))
			r.Post("/{integrationId}/sync", handlers.TriggerJiraSync(d))
		})

		// Nango-managed integrations (OAuth via Nango frontend SDK).
			r.Route("/nango", func(r chi.Router) {
				r.Route("/connections", func(r chi.Router) {
					r.Get("/", handlers.ListNangoConnections(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Post("/", handlers.CreateNangoConnection(d))
					r.With(mw.RequireRole(domain.OrgRoleAdmin)).Delete("/{connectionId}", handlers.DeleteNangoConnection(d))
				})
				r.Post("/sync/{connectionId}", handlers.TriggerNangoSync(d))
			})
		})
	})

	// ─── Internal operator routes ─────────────────────────────────────────────
	r.Route("/operator", func(r chi.Router) {
		r.Use(mw.InternalToken(d.Config.InternalAPIToken))
		r.Use(mw.MaxBodySize(1 << 20)) // 1 MiB for operator payloads

		r.Get("/orgs", handlers.OperatorListOrgs(d))
		r.Get("/orgs/{orgId}", handlers.OperatorGetOrg(d))
		r.Get("/users", handlers.OperatorListUsers(d))
		r.Get("/health", handlers.OperatorHealth(d))
		r.Get("/metrics", handlers.OperatorMetrics())
		// Feature flags.
		r.Get("/flags", handlers.OperatorListFlags(d))
		r.Patch("/flags/{key}", handlers.OperatorUpdateFlag(d))
	})

	return r
}
