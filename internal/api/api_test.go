package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/neuco-ai/neuco/internal/api"
	mw "github.com/neuco-ai/neuco/internal/api/middleware"
	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/jobs"
	"github.com/neuco-ai/neuco/internal/store"
)

// ─── Constants ─────────────────────────────────────────────────────────────────

const (
	testJWTSecret      = "test-jwt-secret-that-is-at-least-32-chars-long"
	testInternalToken  = "test-operator-token-abc123"
	testFrontendURL    = "http://localhost:5173"
)

// ─── Test Setup ────────────────────────────────────────────────────────────────

type testEnv struct {
	server  *httptest.Server
	store   *store.Store
	pool    *pgxpool.Pool
	config  *config.Config
	cleanup func()
}

// testSetup creates a complete test environment with database, store, river
// client, router, and httptest server. Call env.cleanup() when done.
func testSetup(t *testing.T) *testEnv {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://neuco:neuco@localhost:5432/neuco_test?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping integration test: cannot connect to database: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping integration test: cannot ping database: %v", err)
	}

	// Run migrations by executing the migration files.
	runMigrations(t, pool)

	cfg := &config.Config{
		Port:                     "0",
		DatabaseURL:              dbURL,
		JWTSecret:                testJWTSecret,
		InternalAPIToken:         testInternalToken,
		FrontendURL:              testFrontendURL,
	}

	s := store.New(pool)

	// River client in insert-only mode — workers must be registered so
	// that River recognises the job kinds during Insert.
	workers := river.NewWorkers()
	jobs.RegisterAllWorkers(workers, s, cfg)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers,
	})
	if err != nil {
		pool.Close()
		t.Fatalf("failed to create river client: %v", err)
	}

	deps := api.NewDeps(s, riverClient, cfg, pool)
	handler := api.NewRouter(deps, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(handler)

	return &testEnv{
		server: server,
		store:  s,
		pool:   pool,
		config: cfg,
		cleanup: func() {
			server.Close()
			cleanDatabase(pool)
			pool.Close()
		},
	}
}

// runMigrations reads and executes migration SQL files against the pool.
func runMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()

	// Drop all tables and recreate from scratch for test isolation.
	cleanDatabase(pool)

	// Migration 000001
	migration1, err := os.ReadFile("../../migrations/000001_initial_schema.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000001: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration1)); err != nil {
		t.Fatalf("failed to run migration 000001: %v", err)
	}

	// Migration 000002 (feature flags)
	migration2, err := os.ReadFile("../../migrations/000002_feature_flags.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000002: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration2)); err != nil {
		t.Fatalf("failed to run migration 000002: %v", err)
	}

	// Migration 000003 (github app installation)
	migration3, err := os.ReadFile("../../migrations/000003_github_app_installation.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000003: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration3)); err != nil {
		t.Fatalf("failed to run migration 000003: %v", err)
	}

	// Migration 000004 (users github token)
	migration4, err := os.ReadFile("../../migrations/000004_users_github_token.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000004: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration4)); err != nil {
		t.Fatalf("failed to run migration 000004: %v", err)
	}

	// Migration 000005 (expand framework check)
	migration5, err := os.ReadFile("../../migrations/000005_expand_framework_check.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000005: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration5)); err != nil {
		t.Fatalf("failed to run migration 000005: %v", err)
	}

	// River queue tables (required for river.Insert calls in handlers).
	// Use the official rivermigrate API to create the correct schema.
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("failed to create river migrator: %v", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		t.Fatalf("failed to run river migrations: %v", err)
	}
	t.Logf("river migrations applied: %d versions", len(res.Versions))
}

// cleanDatabase drops all application tables so each test run starts fresh.
func cleanDatabase(pool *pgxpool.Pool) {
	ctx := context.Background()
	tables := []string{
		"feature_flags",
		"copilot_notes",
		"audit_log",
		"pipeline_tasks",
		"pipeline_runs",
		"generations",
		"specs",
		"candidate_signals",
		"feature_candidates",
		"signals",
		"integrations",
		"projects",
		"org_members",
		"organizations",
		"users",
	}
	for _, t := range tables {
		pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", t))
	}
	// Drop River tables and types.
	for _, t := range []string{"river_client_queue", "river_client", "river_queue", "river_leader", "river_job", "river_migration"} {
		pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", t))
	}
	pool.Exec(ctx, "DROP FUNCTION IF EXISTS river_job_notify CASCADE")
	pool.Exec(ctx, "DROP TYPE IF EXISTS river_job_state CASCADE")
	pool.Exec(ctx, "DROP EXTENSION IF EXISTS vector CASCADE")
}

// ─── JWT Helpers ───────────────────────────────────────────────────────────────

// generateTestJWT creates a valid JWT access token for integration tests.
func generateTestJWT(userID, orgID uuid.UUID, role domain.OrgRole) string {
	now := time.Now()
	claims := mw.NeuClaims{
		UserID: userID.String(),
		OrgID:  orgID.String(),
		Email:  "testuser@example.com",
		Role:   string(role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		panic("generateTestJWT: " + err.Error())
	}
	return token
}

// ─── Seed Helpers ──────────────────────────────────────────────────────────────

type seedData struct {
	userA   domain.User
	userB   domain.User
	orgA    domain.Organization
	orgB    domain.Organization
	tokenA  string // JWT for userA in orgA (owner)
	tokenB  string // JWT for userB in orgB (owner)
}

// seedTestData inserts two users, two orgs, and membership rows. Returns
// tokens for each user scoped to their respective org.
func seedTestData(t *testing.T, s *store.Store) seedData {
	t.Helper()
	ctx := context.Background()

	userA, err := s.UpsertUser(ctx, 1001, "alice", "alice@example.com", "https://avatar.example.com/alice")
	if err != nil {
		t.Fatalf("seedTestData: create userA: %v", err)
	}
	userB, err := s.UpsertUser(ctx, 1002, "bob", "bob@example.com", "https://avatar.example.com/bob")
	if err != nil {
		t.Fatalf("seedTestData: create userB: %v", err)
	}

	orgA, err := s.CreateOrg(ctx, "Org Alpha", "org-alpha", domain.OrgPlanStarter)
	if err != nil {
		t.Fatalf("seedTestData: create orgA: %v", err)
	}
	orgB, err := s.CreateOrg(ctx, "Org Beta", "org-beta", domain.OrgPlanStarter)
	if err != nil {
		t.Fatalf("seedTestData: create orgB: %v", err)
	}

	if _, err := s.AddMember(ctx, orgA.ID, userA.ID, domain.OrgRoleOwner); err != nil {
		t.Fatalf("seedTestData: add userA to orgA: %v", err)
	}
	if _, err := s.AddMember(ctx, orgB.ID, userB.ID, domain.OrgRoleOwner); err != nil {
		t.Fatalf("seedTestData: add userB to orgB: %v", err)
	}

	return seedData{
		userA:  userA,
		userB:  userB,
		orgA:   orgA,
		orgB:   orgB,
		tokenA: generateTestJWT(userA.ID, orgA.ID, domain.OrgRoleOwner),
		tokenB: generateTestJWT(userB.ID, orgB.ID, domain.OrgRoleOwner),
	}
}

// ─── HTTP Helpers ──────────────────────────────────────────────────────────────

func doRequest(t *testing.T, method, url string, body any, token string) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("doRequest: marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("doRequest: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("doRequest: do: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	return b
}

func assertStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		body := readBody(t, resp)
		t.Fatalf("expected status %d, got %d; body: %s", expected, resp.StatusCode, string(body))
	}
}

// ─── Tests ─────────────────────────────────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	resp := doRequest(t, http.MethodGet, env.server.URL+"/operator/health", nil, testInternalToken)
	assertStatus(t, resp, http.StatusOK)

	var result map[string]any
	json.Unmarshal(readBody(t, resp), &result)
	if result["status"] != "ok" {
		t.Errorf("expected health status ok, got %v", result["status"])
	}
}

func TestAuthFlow(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	t.Run("me_returns_user_and_orgs", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/auth/me", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		body := readBody(t, resp)
		var result struct {
			User domain.User           `json:"user"`
			Orgs []domain.Organization `json:"orgs"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("unmarshal /me response: %v", err)
		}
		if result.User.ID != sd.userA.ID {
			t.Errorf("expected user ID %s, got %s", sd.userA.ID, result.User.ID)
		}
		if len(result.Orgs) == 0 {
			t.Error("expected at least one org")
		}
	})

	t.Run("missing_auth_returns_401", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/auth/me", nil, "")
		assertStatus(t, resp, http.StatusUnauthorized)
	})

	t.Run("invalid_token_returns_401", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/auth/me", nil, "totally.invalid.token")
		assertStatus(t, resp, http.StatusUnauthorized)
	})

	t.Run("refresh_without_body_returns_400", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/auth/refresh", map[string]string{}, "")
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

func TestOrgCRUD(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	t.Run("list_orgs", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var orgs []domain.Organization
		json.Unmarshal(readBody(t, resp), &orgs)
		if len(orgs) == 0 {
			t.Error("expected at least one org")
		}
	})

	t.Run("create_org", func(t *testing.T) {
		payload := map[string]string{
			"name": "New Test Org",
			"slug": "new-test-org-" + uuid.New().String()[:8],
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusCreated)

		var org domain.Organization
		json.Unmarshal(readBody(t, resp), &org)
		if org.Name != payload["name"] {
			t.Errorf("expected org name %q, got %q", payload["name"], org.Name)
		}
	})

	t.Run("get_org_by_id", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		body := readBody(t, resp)
		var org domain.Organization
		json.Unmarshal(body, &org)
		if org.ID != sd.orgA.ID {
			t.Errorf("expected org ID %s, got %s", sd.orgA.ID, org.ID)
		}
	})

	t.Run("update_org", func(t *testing.T) {
		payload := map[string]string{"name": "Updated Alpha Org"}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), payload, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var org domain.Organization
		json.Unmarshal(readBody(t, resp), &org)
		if org.Name != "Updated Alpha Org" {
			t.Errorf("expected updated name, got %q", org.Name)
		}
	})
}

func TestProjectCRUD(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	var createdProjectID string

	t.Run("create_project", func(t *testing.T) {
		payload := map[string]string{
			"name":      "Test Project",
			"framework": "react",
			"styling":   "tailwind",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusCreated)

		var project domain.Project
		json.Unmarshal(readBody(t, resp), &project)
		if project.Name != "Test Project" {
			t.Errorf("expected project name 'Test Project', got %q", project.Name)
		}
		createdProjectID = project.ID.String()
	})

	t.Run("list_projects", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var projects []domain.Project
		json.Unmarshal(readBody(t, resp), &projects)
		if len(projects) == 0 {
			t.Error("expected at least one project")
		}
	})

	t.Run("get_project", func(t *testing.T) {
		if createdProjectID == "" {
			t.Skip("no project created")
		}
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+createdProjectID, nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("update_project", func(t *testing.T) {
		if createdProjectID == "" {
			t.Skip("no project created")
		}
		payload := map[string]string{"name": "Renamed Project"}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/api/v1/projects/"+createdProjectID, payload, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var project domain.Project
		json.Unmarshal(readBody(t, resp), &project)
		if project.Name != "Renamed Project" {
			t.Errorf("expected 'Renamed Project', got %q", project.Name)
		}
	})
}

func TestSignalUpload(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	// Create a project first.
	project, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"Signal Test Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Run("upload_csv_signals", func(t *testing.T) {
		csvData := "content,type,source\n\"Users want dark mode\",feature_request,csv\n\"Login page crashes on Safari\",bug_report,csv"

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		part, err := writer.CreateFormFile("file", "signals.csv")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		part.Write([]byte(csvData))
		writer.Close()

		req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/signals/upload", &buf)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+sd.tokenA)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		assertStatus(t, resp, http.StatusCreated)

		body := readBody(t, resp)
		var result struct {
			Inserted int `json:"inserted"`
		}
		json.Unmarshal(body, &result)
		if result.Inserted < 1 {
			t.Errorf("expected at least 1 inserted signal, got %d", result.Inserted)
		}
	})

	t.Run("list_signals_after_upload", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/signals", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})
}

func TestCandidateRefresh(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	project, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"Candidate Test Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Run("trigger_refresh_creates_pipeline", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/candidates/refresh", nil, sd.tokenA)
		// Should succeed or return a known status (the handler creates a pipeline run).
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
			body := readBody(t, resp)
			t.Fatalf("unexpected status %d; body: %s", resp.StatusCode, string(body))
		}
	})
}

func TestSpecGeneration(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	project, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"Spec Test Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Insert a candidate directly via the store.
	ctx := context.Background()
	_, err = env.pool.Exec(ctx, `
		INSERT INTO feature_candidates (id, project_id, title, problem_summary, signal_count, score, status)
		VALUES ($1, $2, 'Dark Mode', 'Users want dark mode', 5, 0.85, 'new')`,
		uuid.New(), project.ID,
	)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}

	t.Run("list_candidates", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/candidates", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		body := readBody(t, resp)
		var page struct {
			Candidates []map[string]any `json:"candidates"`
			Total      int              `json:"total"`
		}
		json.Unmarshal(body, &page)
		if len(page.Candidates) == 0 {
			t.Error("expected at least one candidate")
		}
	})

	t.Run("generate_spec_for_candidate", func(t *testing.T) {
		// List candidates to get the ID.
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/candidates", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
		var page struct {
			Candidates []struct {
				ID string `json:"id"`
			} `json:"candidates"`
		}
		json.Unmarshal(readBody(t, resp), &page)
		if len(page.Candidates) == 0 {
			t.Skip("no candidates to test spec generation")
		}

		cid := page.Candidates[0].ID
		resp = doRequest(t, http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/candidates/"+cid+"/spec/generate", nil, sd.tokenA)
		// This creates a pipeline run; it may return 200, 201, or 202.
		if resp.StatusCode >= 400 {
			body := readBody(t, resp)
			t.Fatalf("spec generation failed with status %d; body: %s", resp.StatusCode, string(body))
		}
	})
}

func TestPipelineVisibility(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	project, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"Pipeline Test Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a pipeline run directly in the database.
	ctx := context.Background()
	run, err := env.store.CreatePipelineRun(ctx, project.ID, domain.PipelineTypeIngest, nil)
	if err != nil {
		t.Fatalf("create pipeline run: %v", err)
	}

	t.Run("list_pipelines", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/pipelines", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		body := readBody(t, resp)
		var page struct {
			Runs  []map[string]any `json:"runs"`
			Total int              `json:"total"`
		}
		json.Unmarshal(body, &page)
		if len(page.Runs) == 0 {
			t.Error("expected at least one pipeline run")
		}
	})

	t.Run("get_pipeline_detail", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/pipelines/"+run.ID.String(), nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})
}

func TestWebhookIngestion(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	project, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"Webhook Test Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a webhook integration with a known secret.
	ctx := context.Background()
	webhookSecret := "test-webhook-secret-12345"
	intg := domain.Integration{
		ProjectID:     project.ID,
		Provider:      "webhook",
		WebhookSecret: webhookSecret,
		Config:        map[string]any{},
		IsActive:      true,
	}
	_, err = env.store.CreateIntegration(ctx, intg)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	t.Run("valid_webhook_creates_signal", func(t *testing.T) {
		payload := map[string]any{
			"content": "Customer requested dark mode during demo call",
			"type":    "feature_request",
			"source":  "gong",
			"meta": map[string]any{
				"customer": "Acme Corp",
			},
		}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, webhookSecret)
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusCreated)

		body := readBody(t, resp)
		var result struct {
			SignalID string `json:"signal_id"`
			Status   string `json:"status"`
		}
		json.Unmarshal(body, &result)
		if result.Status != "accepted" {
			t.Errorf("expected status 'accepted', got %q", result.Status)
		}
		if result.SignalID == "" {
			t.Error("expected non-empty signal_id")
		}
	})

	t.Run("invalid_secret_returns_401", func(t *testing.T) {
		payload := map[string]any{"content": "test signal"}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, "wrong-secret")
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusUnauthorized)
	})

	t.Run("missing_content_returns_400", func(t *testing.T) {
		payload := map[string]any{"type": "feature_request"}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, webhookSecret)
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("invalid_project_id_returns_400", func(t *testing.T) {
		payload := map[string]any{"content": "test"}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, "not-a-uuid", webhookSecret)
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

func TestRBACEnforcement(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	// Add a viewer to orgA.
	viewerUser, err := env.store.UpsertUser(context.Background(), 2001, "viewer", "viewer@example.com", "")
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := env.store.AddMember(context.Background(), sd.orgA.ID, viewerUser.ID, domain.OrgRoleViewer); err != nil {
		t.Fatalf("add viewer to orgA: %v", err)
	}
	viewerToken := generateTestJWT(viewerUser.ID, sd.orgA.ID, domain.OrgRoleViewer)

	// Add a member to orgA.
	memberUser, err := env.store.UpsertUser(context.Background(), 2002, "member", "member@example.com", "")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, err := env.store.AddMember(context.Background(), sd.orgA.ID, memberUser.ID, domain.OrgRoleMember); err != nil {
		t.Fatalf("add member to orgA: %v", err)
	}
	memberToken := generateTestJWT(memberUser.ID, sd.orgA.ID, domain.OrgRoleMember)

	t.Run("viewer_cannot_create_project", func(t *testing.T) {
		payload := map[string]string{
			"name":      "Viewer Project",
			"framework": "react",
			"styling":   "tailwind",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", payload, viewerToken)
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("member_can_create_project", func(t *testing.T) {
		payload := map[string]string{
			"name":      "Member Project",
			"framework": "react",
			"styling":   "tailwind",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", payload, memberToken)
		assertStatus(t, resp, http.StatusCreated)
	})

	t.Run("viewer_cannot_update_org", func(t *testing.T) {
		payload := map[string]string{"name": "Hacked Org"}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), payload, viewerToken)
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("owner_can_update_org", func(t *testing.T) {
		payload := map[string]string{"name": "Owner Updated Org"}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), payload, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("viewer_cannot_invite_member", func(t *testing.T) {
		payload := map[string]string{"email": "new@example.com", "role": "member"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/members/invite", payload, viewerToken)
		assertStatus(t, resp, http.StatusForbidden)
	})
}

func TestTenantIsolation(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	// Create a project in orgA.
	projectA, err := env.store.CreateProject(
		context.Background(),
		sd.orgA.ID,
		"OrgA Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userA.ID,
	)
	if err != nil {
		t.Fatalf("create projectA: %v", err)
	}

	// Create a project in orgB.
	projectB, err := env.store.CreateProject(
		context.Background(),
		sd.orgB.ID,
		"OrgB Project",
		"",
		domain.ProjectFrameworkReact,
		domain.ProjectStylingTailwind,
		sd.userB.ID,
	)
	if err != nil {
		t.Fatalf("create projectB: %v", err)
	}

	t.Run("orgA_cannot_see_orgB_project", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectB.ID.String(), nil, sd.tokenA)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("orgB_cannot_see_orgA_project", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectA.ID.String(), nil, sd.tokenB)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("orgA_can_see_own_project", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectA.ID.String(), nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("orgB_can_see_own_project", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectB.ID.String(), nil, sd.tokenB)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("orgA_cannot_list_orgB_signals", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectB.ID.String()+"/signals", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("orgA_cannot_list_orgB_pipelines", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+projectB.ID.String()+"/pipelines", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("orgB_cannot_delete_orgA_project", func(t *testing.T) {
		// userB with orgB token should NOT be able to delete projectA in orgA.
		resp := doRequest(t, http.MethodDelete, env.server.URL+"/api/v1/projects/"+projectA.ID.String(), nil, sd.tokenB)
		assertStatus(t, resp, http.StatusNotFound)
	})
}

func TestOperatorFlagsCRUD(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	t.Run("list_flags", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/operator/flags", nil, testInternalToken)
		assertStatus(t, resp, http.StatusOK)

		var flags []map[string]any
		json.Unmarshal(readBody(t, resp), &flags)
		if len(flags) == 0 {
			t.Error("expected seeded feature flags")
		}

		// Verify we have the expected keys from the migration.
		keys := make(map[string]bool)
		for _, f := range flags {
			keys[f["key"].(string)] = true
		}
		expected := []string{"eino_integration", "rlm_agent", "github_app", "email_notifications", "natural_language_query", "weekly_digest"}
		for _, k := range expected {
			if !keys[k] {
				t.Errorf("expected flag key %q in result", k)
			}
		}
	})

	t.Run("toggle_flag_on", func(t *testing.T) {
		payload := map[string]bool{"enabled": true}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/operator/flags/github_app", payload, testInternalToken)
		assertStatus(t, resp, http.StatusOK)

		var flag map[string]any
		json.Unmarshal(readBody(t, resp), &flag)
		if flag["enabled"] != true {
			t.Error("expected flag to be enabled")
		}
	})

	t.Run("toggle_flag_off", func(t *testing.T) {
		payload := map[string]bool{"enabled": false}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/operator/flags/weekly_digest", payload, testInternalToken)
		assertStatus(t, resp, http.StatusOK)

		var flag map[string]any
		json.Unmarshal(readBody(t, resp), &flag)
		if flag["enabled"] != false {
			t.Error("expected flag to be disabled")
		}
	})

	t.Run("toggle_nonexistent_flag_returns_404", func(t *testing.T) {
		payload := map[string]bool{"enabled": true}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/operator/flags/does_not_exist", payload, testInternalToken)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("operator_auth_required", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/operator/flags", nil, "")
		assertStatus(t, resp, http.StatusUnauthorized)
	})

	t.Run("wrong_operator_token_returns_401", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/operator/flags", nil, "wrong-token")
		assertStatus(t, resp, http.StatusUnauthorized)
	})
}

// Suppress unused import warnings -- these are used through type assertions.
var (
	_ = strings.NewReader
	_ pgx.Tx
)
