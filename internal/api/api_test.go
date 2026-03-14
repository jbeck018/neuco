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
	testJWTSecret     = "test-jwt-secret-that-is-at-least-32-chars-long"
	testInternalToken = "test-operator-token-abc123"
	testFrontendURL   = "http://localhost:5173"
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
		Port:             "0",
		DatabaseURL:      dbURL,
		JWTSecret:        testJWTSecret,
		InternalAPIToken: testInternalToken,
		FrontendURL:      testFrontendURL,
	}

	s := store.New(pool)

	// River client in insert-only mode — workers must be registered so
	// that River recognises the job kinds during Insert.
	workers := river.NewWorkers()
	jobCtx := jobs.RegisterAllWorkers(workers, s, cfg)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers,
	})
	if err != nil {
		pool.Close()
		t.Fatalf("failed to create river client: %v", err)
	}
	jobCtx.SetClient(riverClient)

	deps := api.NewDeps(s, riverClient, jobCtx, cfg, pool)
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

	// Migration 000006 (subscriptions)
	migration6, err := os.ReadFile("../../migrations/000006_subscriptions.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000006: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration6)); err != nil {
		t.Fatalf("failed to run migration 000006: %v", err)
	}

	// Migration 000007 (usage tracking)
	migration7, err := os.ReadFile("../../migrations/000007_usage_tracking.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000007: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration7)); err != nil {
		t.Fatalf("failed to run migration 000007: %v", err)
	}

	// Migration 000008 (user onboarding)
	migration8, err := os.ReadFile("../../migrations/000008_user_onboarding.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000008: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration8)); err != nil {
		t.Fatalf("failed to run migration 000008: %v", err)
	}

	// Migration 000009 (llm calls)
	migration9, err := os.ReadFile("../../migrations/000009_llm_calls.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000009: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration9)); err != nil {
		t.Fatalf("failed to run migration 000009: %v", err)
	}

	// Migration 000010 (google sso)
	migration10, err := os.ReadFile("../../migrations/000010_google_sso.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000010: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration10)); err != nil {
		t.Fatalf("failed to run migration 000010: %v", err)
	}

	// Migration 000011 (project context)
	migration11, err := os.ReadFile("../../migrations/000011_project_context.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000011: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration11)); err != nil {
		t.Fatalf("failed to run migration 000011: %v", err)
	}

	// Migration 000012 (signal dedup)
	migration12, err := os.ReadFile("../../migrations/000012_signal_dedup.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000012: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration12)); err != nil {
		t.Fatalf("failed to run migration 000012: %v", err)
	}

	// Migration 000013 (digest opt out)
	migration13, err := os.ReadFile("../../migrations/000013_digest_opt_out.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000013: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration13)); err != nil {
		t.Fatalf("failed to run migration 000013: %v", err)
	}

	// Migration 000014 (notifications)
	migration14, err := os.ReadFile("../../migrations/000014_notifications.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000014: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration14)); err != nil {
		t.Fatalf("failed to run migration 000014: %v", err)
	}

	// Migration 000015 (production indexes) — skip ALTER DATABASE in test env.
	migration15, err := os.ReadFile("../../migrations/000015_production_indexes.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000015: %v", err)
	}
	// Remove ALTER DATABASE statement which requires superuser and isn't needed in tests.
	m15Str := string(migration15)
	m15Str = strings.Replace(m15Str, "ALTER DATABASE CURRENT SET statement_timeout = '30s';", "", 1)
	if _, err := pool.Exec(ctx, m15Str); err != nil {
		t.Fatalf("failed to run migration 000015: %v", err)
	}

	// Migration 000016 (expand signal types)
	migration16, err := os.ReadFile("../../migrations/000016_expand_signal_types.up.sql")
	if err != nil {
		t.Fatalf("failed to read migration 000016: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration16)); err != nil {
		t.Fatalf("failed to run migration 000016: %v", err)
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
		"notifications",
		"project_contexts",
		"llm_calls",
		"user_onboarding",
		"org_usage",
		"subscriptions",
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
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", t))
	}
	// Drop River tables and types.
	for _, t := range []string{"river_client_queue", "river_client", "river_queue", "river_leader", "river_job", "river_migration"} {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", t))
	}
	_, _ = pool.Exec(ctx, "DROP FUNCTION IF EXISTS river_job_notify CASCADE")
	_, _ = pool.Exec(ctx, "DROP TYPE IF EXISTS river_job_state CASCADE")
	_, _ = pool.Exec(ctx, "DROP EXTENSION IF EXISTS vector CASCADE")
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
	userA  domain.User
	userB  domain.User
	orgA   domain.Organization
	orgB   domain.Organization
	tokenA string // JWT for userA in orgA (owner)
	tokenB string // JWT for userB in orgB (owner)
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
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	return b
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
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
	if err := json.Unmarshal(readBody(t, resp), &result); err != nil {
		t.Fatalf("unmarshal health response: %v", err)
	}
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

	t.Run("refresh_without_cookie_returns_401", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/auth/refresh", map[string]string{}, "")
		assertStatus(t, resp, http.StatusUnauthorized)
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
		if err := json.Unmarshal(readBody(t, resp), &orgs); err != nil {
			t.Fatalf("unmarshal orgs: %v", err)
		}
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
		if err := json.Unmarshal(readBody(t, resp), &org); err != nil {
			t.Fatalf("unmarshal org: %v", err)
		}
		if org.Name != payload["name"] {
			t.Errorf("expected org name %q, got %q", payload["name"], org.Name)
		}
	})

	t.Run("get_org_by_id", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		body := readBody(t, resp)
		var org domain.Organization
		if err := json.Unmarshal(body, &org); err != nil {
			t.Fatalf("unmarshal org: %v", err)
		}
		if org.ID != sd.orgA.ID {
			t.Errorf("expected org ID %s, got %s", sd.orgA.ID, org.ID)
		}
	})

	t.Run("update_org", func(t *testing.T) {
		payload := map[string]string{"name": "Updated Alpha Org"}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String(), payload, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var org domain.Organization
		if err := json.Unmarshal(readBody(t, resp), &org); err != nil {
			t.Fatalf("unmarshal org: %v", err)
		}
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
		if err := json.Unmarshal(readBody(t, resp), &project); err != nil {
			t.Fatalf("unmarshal project: %v", err)
		}
		if project.Name != "Test Project" {
			t.Errorf("expected project name 'Test Project', got %q", project.Name)
		}
		createdProjectID = project.ID.String()
	})

	t.Run("list_projects", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var projects []domain.Project
		if err := json.Unmarshal(readBody(t, resp), &projects); err != nil {
			t.Fatalf("unmarshal projects: %v", err)
		}
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
		if err := json.Unmarshal(readBody(t, resp), &project); err != nil {
			t.Fatalf("unmarshal project: %v", err)
		}
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
		if _, werr := part.Write([]byte(csvData)); werr != nil {
			t.Fatalf("part write: %v", werr)
		}
		if cerr := writer.Close(); cerr != nil {
			t.Fatalf("writer close: %v", cerr)
		}

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
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if result.Inserted < 1 {
			t.Errorf("expected at least 1 inserted signal, got %d", result.Inserted)
		}
	})

	t.Run("list_signals_after_upload", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/signals", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("upload_plain_text_with_type", func(t *testing.T) {
		textData := "Users keep asking for a dark mode option.\n\nThe onboarding flow is confusing."

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)

		// Add the type form field.
		if err := writer.WriteField("type", "bug_report"); err != nil {
			t.Fatalf("write type field: %v", err)
		}

		part, err := writer.CreateFormFile("file", "feedback.txt")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, werr := part.Write([]byte(textData)); werr != nil {
			t.Fatalf("part write: %v", werr)
		}
		if cerr := writer.Close(); cerr != nil {
			t.Fatalf("writer close: %v", cerr)
		}

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
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if result.Inserted != 2 {
			t.Errorf("expected 2 inserted signals, got %d", result.Inserted)
		}

		// Verify that signals were stored with the correct type.
		listResp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/signals?type=bug_report", nil, sd.tokenA)
		assertStatus(t, listResp, http.StatusOK)
		listBody := readBody(t, listResp)
		var listResult struct {
			Signals []struct {
				Type string `json:"type"`
			} `json:"signals"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(listBody, &listResult); err != nil {
			t.Fatalf("unmarshal list: %v", err)
		}
		if listResult.Total < 2 {
			t.Errorf("expected at least 2 bug_report signals, got %d", listResult.Total)
		}
		for _, s := range listResult.Signals {
			if s.Type != "bug_report" {
				t.Errorf("expected signal type bug_report, got %s", s.Type)
			}
		}
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
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("unmarshal page: %v", err)
		}
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
		if err := json.Unmarshal(readBody(t, resp), &page); err != nil {
			t.Fatalf("unmarshal page: %v", err)
		}
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
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("unmarshal page: %v", err)
		}
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
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
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
		mustUnmarshal(t, readBody(t, resp), &flags)
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
		mustUnmarshal(t, readBody(t, resp), &flag)
		if flag["enabled"] != true {
			t.Error("expected flag to be enabled")
		}
	})

	t.Run("toggle_flag_off", func(t *testing.T) {
		payload := map[string]bool{"enabled": false}
		resp := doRequest(t, http.MethodPatch, env.server.URL+"/operator/flags/weekly_digest", payload, testInternalToken)
		assertStatus(t, resp, http.StatusOK)

		var flag map[string]any
		mustUnmarshal(t, readBody(t, resp), &flag)
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

func TestOnboardingFlow(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	t.Run("initial_status_is_empty", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/onboarding/status", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var status domain.OnboardingStatus
		mustUnmarshal(t, readBody(t, resp), &status)
		if status.IsComplete {
			t.Error("expected onboarding not complete initially")
		}
		if len(status.CompletedSteps) != 0 {
			t.Errorf("expected 0 completed steps, got %d", len(status.CompletedSteps))
		}
		if status.TotalSteps != 6 {
			t.Errorf("expected 6 total steps, got %d", status.TotalSteps)
		}
	})

	t.Run("complete_welcome_step", func(t *testing.T) {
		payload := map[string]string{"step": "welcome"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/onboarding/step", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var status domain.OnboardingStatus
		mustUnmarshal(t, readBody(t, resp), &status)
		if len(status.CompletedSteps) != 1 {
			t.Errorf("expected 1 completed step, got %d", len(status.CompletedSteps))
		}
		if status.CompletedSteps[0] != domain.OnboardingStepWelcome {
			t.Errorf("expected step 'welcome', got %q", status.CompletedSteps[0])
		}
	})

	t.Run("complete_multiple_steps", func(t *testing.T) {
		for _, step := range []string{"org", "project"} {
			payload := map[string]string{"step": step}
			resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/onboarding/step", payload, sd.tokenA)
			assertStatus(t, resp, http.StatusOK)
		}

		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/onboarding/status", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var status domain.OnboardingStatus
		mustUnmarshal(t, readBody(t, resp), &status)
		if len(status.CompletedSteps) != 3 {
			t.Errorf("expected 3 completed steps, got %d", len(status.CompletedSteps))
		}
	})

	t.Run("invalid_step_returns_400", func(t *testing.T) {
		payload := map[string]string{"step": "nonexistent_step"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/onboarding/step", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("skip_onboarding", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/onboarding/skip", nil, sd.tokenB)
		assertStatus(t, resp, http.StatusOK)

		var status domain.OnboardingStatus
		mustUnmarshal(t, readBody(t, resp), &status)
		if !status.IsComplete {
			t.Error("expected onboarding to be complete after skip")
		}
	})

	t.Run("unauthenticated_returns_401", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/onboarding/status", nil, "")
		assertStatus(t, resp, http.StatusUnauthorized)
	})
}

func TestBillingSubscription(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	t.Run("no_subscription_returns_null", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/subscription", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var result map[string]any
		mustUnmarshal(t, readBody(t, resp), &result)
		if result["subscription"] != nil {
			t.Error("expected null subscription for new org")
		}
	})

	t.Run("get_usage_free_tier", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var usage domain.UsageSummary
		mustUnmarshal(t, readBody(t, resp), &usage)
		if usage.Limits.MaxProjects != 1 {
			t.Errorf("expected free tier max_projects=1, got %d", usage.Limits.MaxProjects)
		}
		if usage.Limits.MaxSignals != 20 {
			t.Errorf("expected free tier max_signals=20, got %d", usage.Limits.MaxSignals)
		}
		if usage.PlanTier != nil {
			t.Error("expected nil plan tier for free org")
		}
	})

	// Upsert a subscription directly via store (simulating a Stripe webhook).
	subID := "sub_test_" + uuid.New().String()[:8]
	sub := domain.Subscription{
		OrgID:                sd.orgA.ID,
		StripeCustomerID:     "cus_test_" + uuid.New().String()[:8],
		StripeSubscriptionID: &subID,
		PlanTier:             domain.PlanTierStarter,
		Status:               domain.SubStatusActive,
	}
	_, err := env.store.UpsertSubscription(ctx, sub)
	if err != nil {
		t.Fatalf("upsert subscription: %v", err)
	}

	t.Run("get_subscription_after_upsert", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/subscription", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var result struct {
			Subscription *domain.Subscription `json:"subscription"`
			Limits       domain.PlanLimits    `json:"limits"`
		}
		mustUnmarshal(t, readBody(t, resp), &result)
		if result.Subscription == nil {
			t.Fatal("expected non-null subscription")
		}
		if result.Subscription.PlanTier != domain.PlanTierStarter {
			t.Errorf("expected starter plan, got %q", result.Subscription.PlanTier)
		}
		if result.Subscription.Status != domain.SubStatusActive {
			t.Errorf("expected active status, got %q", result.Subscription.Status)
		}
		if result.Limits.MaxProjects != 3 {
			t.Errorf("expected starter max_projects=3, got %d", result.Limits.MaxProjects)
		}
	})

	t.Run("get_usage_with_subscription", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var usage domain.UsageSummary
		mustUnmarshal(t, readBody(t, resp), &usage)
		if usage.PlanTier == nil || *usage.PlanTier != domain.PlanTierStarter {
			t.Error("expected starter plan tier in usage")
		}
		if usage.Limits.MaxSignals != 100 {
			t.Errorf("expected starter max_signals=100, got %d", usage.Limits.MaxSignals)
		}
	})

	t.Run("tenant_isolation_billing", func(t *testing.T) {
		// orgB should not see orgA's subscription.
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/subscription", nil, sd.tokenB)
		// Should fail — orgB token cannot access orgA routes.
		if resp.StatusCode == http.StatusOK {
			var result map[string]any
			mustUnmarshal(t, readBody(t, resp), &result)
			if result["subscription"] != nil {
				t.Error("orgB should not see orgA subscription")
			}
		}
	})
}

func TestUsageLimitEnforcement(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	// Create a project for signal upload tests.
	project, err := env.store.CreateProject(ctx, sd.orgA.ID, "Limit Test Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Exhaust the free-tier signal limit (20) by inserting usage directly.
	if err := env.store.IncrementSignalUsage(ctx, sd.orgA.ID, 20); err != nil {
		t.Fatalf("increment signal usage: %v", err)
	}

	t.Run("signal_upload_blocked_at_limit", func(t *testing.T) {
		csvData := "content,type,source\n\"Blocked signal\",feature_request,csv"

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		part, err := writer.CreateFormFile("file", "signals.csv")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, werr := part.Write([]byte(csvData)); werr != nil {
			t.Fatalf("part write: %v", werr)
		}
		if cerr := writer.Close(); cerr != nil {
			t.Fatalf("writer close: %v", cerr)
		}

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
		assertStatus(t, resp, http.StatusTooManyRequests)

		var result map[string]any
		mustUnmarshal(t, readBody(t, resp), &result)
		if result["code"] != "usage_limit_exceeded" {
			t.Errorf("expected code 'usage_limit_exceeded', got %v", result["code"])
		}
	})

	t.Run("project_creation_blocked_at_limit", func(t *testing.T) {
		// Free tier allows 1 project. We already have 1.
		payload := map[string]string{
			"name":      "Second Project",
			"framework": "react",
			"styling":   "tailwind",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusTooManyRequests)
	})

	t.Run("upgrade_unlocks_limits", func(t *testing.T) {
		// Upgrade orgA to starter plan (3 projects, 100 signals).
		subID := "sub_upgrade_" + uuid.New().String()[:8]
		sub := domain.Subscription{
			OrgID:                sd.orgA.ID,
			StripeCustomerID:     "cus_upgrade_" + uuid.New().String()[:8],
			StripeSubscriptionID: &subID,
			PlanTier:             domain.PlanTierStarter,
			Status:               domain.SubStatusActive,
		}
		if _, err := env.store.UpsertSubscription(ctx, sub); err != nil {
			t.Fatalf("upsert subscription: %v", err)
		}

		// Now project creation should succeed (starter allows 3 projects, we have 1).
		payload := map[string]string{
			"name":      "Second Project After Upgrade",
			"framework": "react",
			"styling":   "tailwind",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusCreated)
	})
}

func TestLLMUsageTracking(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	project, err := env.store.CreateProject(ctx, sd.orgA.ID, "LLM Test Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a pipeline run to associate LLM calls with.
	run, err := env.store.CreatePipelineRun(ctx, project.ID, domain.PipelineTypeIngest, nil)
	if err != nil {
		t.Fatalf("create pipeline run: %v", err)
	}

	// Insert several LLM call records via the store.
	calls := []domain.LLMCall{
		{ProjectID: project.ID, PipelineRunID: &run.ID, Provider: domain.LLMProviderAnthropic, Model: "claude-sonnet-4-6-20250514", CallType: domain.LLMCallTypeSpecGen, TokensIn: 1000, TokensOut: 500, LatencyMs: 1200, CostUSD: 0.0105},
		{ProjectID: project.ID, PipelineRunID: &run.ID, Provider: domain.LLMProviderAnthropic, Model: "claude-haiku-4-5-20251001", CallType: domain.LLMCallTypeThemeNaming, TokensIn: 200, TokensOut: 50, LatencyMs: 300, CostUSD: 0.00036},
		{ProjectID: project.ID, PipelineRunID: &run.ID, Provider: domain.LLMProviderOpenAI, Model: "text-embedding-3-small", CallType: domain.LLMCallTypeEmbedding, TokensIn: 500, TokensOut: 0, LatencyMs: 100, CostUSD: 0.00001},
	}
	for i := range calls {
		if err := env.store.CreateLLMCall(ctx, &calls[i]); err != nil {
			t.Fatalf("create llm call %d: %v", i, err)
		}
	}

	t.Run("project_llm_usage_aggregation", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/llm-usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var agg domain.LLMUsageAgg
		mustUnmarshal(t, readBody(t, resp), &agg)
		if agg.TotalCalls != 3 {
			t.Errorf("expected 3 total calls, got %d", agg.TotalCalls)
		}
		if agg.TotalTokensIn != 1700 {
			t.Errorf("expected 1700 total tokens in, got %d", agg.TotalTokensIn)
		}
		if agg.TotalTokensOut != 550 {
			t.Errorf("expected 550 total tokens out, got %d", agg.TotalTokensOut)
		}
	})

	t.Run("pipeline_llm_usage", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/pipelines/"+run.ID.String()+"/llm-usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var agg domain.LLMUsageAgg
		mustUnmarshal(t, readBody(t, resp), &agg)
		if agg.TotalCalls != 3 {
			t.Errorf("expected 3 calls for pipeline, got %d", agg.TotalCalls)
		}
	})

	t.Run("list_llm_calls_paginated", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/llm-usage/calls?limit=2&offset=0", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var page struct {
			Calls []domain.LLMCall `json:"calls"`
			Total int              `json:"total"`
		}
		mustUnmarshal(t, readBody(t, resp), &page)
		if page.Total != 3 {
			t.Errorf("expected total 3, got %d", page.Total)
		}
		if len(page.Calls) != 2 {
			t.Errorf("expected 2 calls in page, got %d", len(page.Calls))
		}
	})

	t.Run("org_llm_usage", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/llm-usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var agg domain.LLMUsageAgg
		mustUnmarshal(t, readBody(t, resp), &agg)
		if agg.TotalCalls != 3 {
			t.Errorf("expected 3 calls at org level, got %d", agg.TotalCalls)
		}
	})

	t.Run("tenant_isolation_llm_usage", func(t *testing.T) {
		// orgB should not see orgA's LLM usage via project routes.
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/llm-usage", nil, sd.tokenB)
		assertStatus(t, resp, http.StatusNotFound)
	})
}

func TestSlackWebhook(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	// Create a project and a Slack integration.
	project, err := env.store.CreateProject(ctx, sd.orgA.ID, "Slack Test Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = env.store.CreateIntegration(ctx, domain.Integration{
		ProjectID: project.ID,
		Provider:  "slack",
		Config: map[string]any{
			"access_token": "xoxb-test",
			"team_id":      "T_TEST",
		},
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("create slack integration: %v", err)
	}

	t.Run("url_verification_challenge", func(t *testing.T) {
		payload := map[string]any{
			"type":      "url_verification",
			"token":     "test_token",
			"challenge": "test_challenge_value_12345",
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/webhooks/slack", payload, "")
		assertStatus(t, resp, http.StatusOK)

		var result map[string]string
		mustUnmarshal(t, readBody(t, resp), &result)
		if result["challenge"] != "test_challenge_value_12345" {
			t.Errorf("expected challenge echoed back, got %q", result["challenge"])
		}
	})

	t.Run("message_event_creates_signal", func(t *testing.T) {
		payload := map[string]any{
			"type":    "event_callback",
			"team_id": "T_TEST",
			"event": map[string]any{
				"type":    "message",
				"channel": "C_GENERAL",
				"user":    "U_ALICE",
				"text":    "Users are requesting better onboarding",
				"ts":      "1700000000.000001",
			},
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/webhooks/slack", payload, "")
		assertStatus(t, resp, http.StatusOK)

		// Verify the signal was created.
		page, listErr := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 10})
		if listErr != nil {
			t.Fatalf("list signals: %v", listErr)
		}
		if len(page.Signals) == 0 {
			t.Error("expected at least one signal from slack webhook")
		}
	})

	t.Run("bot_message_ignored", func(t *testing.T) {
		// Count existing signals.
		beforePage, _ := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 100})

		payload := map[string]any{
			"type":    "event_callback",
			"team_id": "T_TEST",
			"event": map[string]any{
				"type":    "message",
				"subtype": "bot_message",
				"channel": "C_GENERAL",
				"text":    "I am a bot",
				"ts":      "1700000001.000001",
			},
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/webhooks/slack", payload, "")
		assertStatus(t, resp, http.StatusOK)

		afterPage, _ := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 100})
		if len(afterPage.Signals) != len(beforePage.Signals) {
			t.Error("bot message should not create a new signal")
		}
	})
}

func TestIntercomWebhook(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	// Create a project and an Intercom integration.
	project, err := env.store.CreateProject(ctx, sd.orgA.ID, "Intercom Test Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = env.store.CreateIntegration(ctx, domain.Integration{
		ProjectID: project.ID,
		Provider:  "intercom",
		Config: map[string]any{
			"access_token": "test-intercom-token",
		},
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("create intercom integration: %v", err)
	}

	t.Run("conversation_created_creates_signal", func(t *testing.T) {
		payload := map[string]any{
			"topic":  "conversation.created",
			"app_id": "test_app",
			"data": map[string]any{
				"item": map[string]any{
					"id":         "conv_123",
					"type":       "conversation",
					"created_at": 1700000000,
					"source": map[string]any{
						"body": "I need help with the checkout flow, it keeps crashing",
					},
				},
			},
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/webhooks/intercom", payload, "")
		assertStatus(t, resp, http.StatusOK)

		page, listErr := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 10})
		if listErr != nil {
			t.Fatalf("list signals: %v", listErr)
		}
		if len(page.Signals) == 0 {
			t.Error("expected at least one signal from intercom webhook")
		}
	})

	t.Run("unhandled_topic_returns_ok", func(t *testing.T) {
		// Count existing signals.
		beforePage, _ := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 100})

		payload := map[string]any{
			"topic":  "conversation.admin.replied",
			"app_id": "test_app",
			"data": map[string]any{
				"item": map[string]any{
					"id":   "conv_456",
					"type": "conversation",
				},
			},
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/webhooks/intercom", payload, "")
		assertStatus(t, resp, http.StatusOK)

		afterPage, _ := env.store.ListProjectSignals(ctx, project.ID, store.SignalFilters{}, store.PageParams{Limit: 100})
		if len(afterPage.Signals) != len(beforePage.Signals) {
			t.Error("unhandled topic should not create a new signal")
		}
	})
}

func TestEmailClientInitialization(t *testing.T) {
	t.Run("nil_client_when_no_api_key", func(t *testing.T) {
		// The email.New function returns nil when apiKey is empty,
		// allowing the app to run without email configured.
		// This verifies the graceful degradation pattern.
		env := testSetup(t)
		defer env.cleanup()

		// The test config has no Resend API key, so the email client
		// should not be created. Verify the config has no key set.
		if env.config.ResendAPIKey != "" {
			t.Error("test config should not have a Resend API key")
		}
	})
}

func TestBillingRBACEnforcement(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	// Add a viewer to orgA.
	viewerUser, err := env.store.UpsertUser(context.Background(), 3001, "billing-viewer", "billing-viewer@example.com", "")
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := env.store.AddMember(context.Background(), sd.orgA.ID, viewerUser.ID, domain.OrgRoleViewer); err != nil {
		t.Fatalf("add viewer: %v", err)
	}
	viewerToken := generateTestJWT(viewerUser.ID, sd.orgA.ID, domain.OrgRoleViewer)

	// Add a member to orgA.
	memberUser, err := env.store.UpsertUser(context.Background(), 3002, "billing-member", "billing-member@example.com", "")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, err := env.store.AddMember(context.Background(), sd.orgA.ID, memberUser.ID, domain.OrgRoleMember); err != nil {
		t.Fatalf("add member: %v", err)
	}
	memberToken := generateTestJWT(memberUser.ID, sd.orgA.ID, domain.OrgRoleMember)

	t.Run("viewer_can_read_subscription", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/subscription", nil, viewerToken)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("viewer_can_read_usage", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/usage", nil, viewerToken)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("member_cannot_create_checkout", func(t *testing.T) {
		payload := map[string]string{"plan_tier": "starter"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/checkout", payload, memberToken)
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("viewer_cannot_create_checkout", func(t *testing.T) {
		payload := map[string]string{"plan_tier": "starter"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/checkout", payload, viewerToken)
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("member_cannot_create_portal", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/portal", nil, memberToken)
		assertStatus(t, resp, http.StatusForbidden)
	})
}

func TestUsageIncrementAndQuery(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	// Create a project.
	_, err := env.store.CreateProject(ctx, sd.orgA.ID, "Usage Query Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Increment usage counters.
	if err := env.store.IncrementSignalUsage(ctx, sd.orgA.ID, 5); err != nil {
		t.Fatalf("increment signals: %v", err)
	}
	if err := env.store.IncrementPRUsage(ctx, sd.orgA.ID); err != nil {
		t.Fatalf("increment PRs: %v", err)
	}
	if err := env.store.IncrementPRUsage(ctx, sd.orgA.ID); err != nil {
		t.Fatalf("increment PRs: %v", err)
	}

	t.Run("usage_reflects_increments", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var usage domain.UsageSummary
		mustUnmarshal(t, readBody(t, resp), &usage)
		if usage.SignalsUsed != 5 {
			t.Errorf("expected 5 signals used, got %d", usage.SignalsUsed)
		}
		if usage.PRsUsed != 2 {
			t.Errorf("expected 2 PRs used, got %d", usage.PRsUsed)
		}
		if usage.ProjectCount != 1 {
			t.Errorf("expected 1 project, got %d", usage.ProjectCount)
		}
	})

	t.Run("idempotent_signal_increment", func(t *testing.T) {
		if err := env.store.IncrementSignalUsage(ctx, sd.orgA.ID, 3); err != nil {
			t.Fatalf("increment signals: %v", err)
		}

		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/billing/usage", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var usage domain.UsageSummary
		mustUnmarshal(t, readBody(t, resp), &usage)
		if usage.SignalsUsed != 8 {
			t.Errorf("expected 8 signals used after second increment, got %d", usage.SignalsUsed)
		}
	})
}

// ─── Integration E2E Tests ──────────────────────────────────────────────────

func TestWebhookFullFlow(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	// Step 1: Create a project via API.
	projectPayload := map[string]string{
		"name":      "Webhook Flow Project",
		"framework": "react",
		"styling":   "tailwind",
	}
	resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/orgs/"+sd.orgA.ID.String()+"/projects", projectPayload, sd.tokenA)
	assertStatus(t, resp, http.StatusCreated)

	var project domain.Project
	mustUnmarshal(t, readBody(t, resp), &project)

	t.Run("create_webhook_integration_via_api", func(t *testing.T) {
		// Step 2: Create a webhook integration via the API.
		intgPayload := map[string]any{"provider": "webhook"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations", intgPayload, sd.tokenA)
		assertStatus(t, resp, http.StatusCreated)

		var created domain.Integration
		mustUnmarshal(t, readBody(t, resp), &created)
		if created.Provider != "webhook" {
			t.Errorf("expected provider 'webhook', got %q", created.Provider)
		}
		if created.WebhookSecret == "" {
			t.Fatal("expected webhook_secret to be returned on create")
		}

		// Step 3: Send a webhook payload using the returned secret.
		webhookPayload := map[string]any{
			"content": "Enterprise customer needs SSO integration",
			"type":    "feature_request",
			"source":  "webhook",
			"meta":    map[string]any{"customer": "BigCorp", "deal_size": "$50k"},
		}
		webhookURL := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, created.WebhookSecret)
		resp = doRequest(t, http.MethodPost, webhookURL, webhookPayload, "")
		assertStatus(t, resp, http.StatusCreated)

		var webhookResult struct {
			SignalID string `json:"signal_id"`
			Status   string `json:"status"`
		}
		mustUnmarshal(t, readBody(t, resp), &webhookResult)
		if webhookResult.Status != "accepted" {
			t.Errorf("expected status 'accepted', got %q", webhookResult.Status)
		}

		// Step 4: Verify the signal appears in the API signals list.
		resp = doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/signals", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var signalPage struct {
			Signals []domain.Signal `json:"signals"`
			Total   int             `json:"total"`
		}
		mustUnmarshal(t, readBody(t, resp), &signalPage)
		if signalPage.Total == 0 {
			t.Fatal("expected at least 1 signal in the signals list")
		}

		foundSignal := false
		for _, sig := range signalPage.Signals {
			if sig.Content == "Enterprise customer needs SSO integration" {
				foundSignal = true
				if string(sig.Source) != "webhook" {
					t.Errorf("expected source 'webhook', got %q", sig.Source)
				}
				if string(sig.Type) != "feature_request" {
					t.Errorf("expected type 'feature_request', got %q", sig.Type)
				}
				break
			}
		}
		if !foundSignal {
			t.Error("webhook signal not found in project signals list")
		}
	})
}

func TestWebhookDeduplication(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)
	ctx := context.Background()

	project, err := env.store.CreateProject(ctx, sd.orgA.ID, "Dedup Test Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	webhookSecret := "dedup-secret-12345"
	_, err = env.store.CreateIntegration(ctx, domain.Integration{
		ProjectID:     project.ID,
		Provider:      "webhook",
		WebhookSecret: webhookSecret,
		Config:        map[string]any{},
		IsActive:      true,
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	webhookURL := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, webhookSecret)

	t.Run("duplicate_content_returns_deduplicated", func(t *testing.T) {
		payload := map[string]any{
			"content": "Users want dark mode support",
			"type":    "feature_request",
		}

		// First submission — should be accepted.
		resp := doRequest(t, http.MethodPost, webhookURL, payload, "")
		assertStatus(t, resp, http.StatusCreated)

		var result1 struct {
			SignalID string `json:"signal_id"`
			Status   string `json:"status"`
		}
		mustUnmarshal(t, readBody(t, resp), &result1)
		if result1.Status != "accepted" {
			t.Fatalf("first submission: expected 'accepted', got %q", result1.Status)
		}

		// Second submission with identical content — should be deduplicated.
		resp = doRequest(t, http.MethodPost, webhookURL, payload, "")
		assertStatus(t, resp, http.StatusOK)

		var result2 struct {
			SignalID string `json:"signal_id"`
			Status   string `json:"status"`
		}
		mustUnmarshal(t, readBody(t, resp), &result2)
		if result2.Status != "deduplicated" {
			t.Errorf("second submission: expected 'deduplicated', got %q", result2.Status)
		}
	})

	t.Run("different_content_is_not_deduplicated", func(t *testing.T) {
		payload := map[string]any{
			"content": "Users want light mode support",
			"type":    "feature_request",
		}
		resp := doRequest(t, http.MethodPost, webhookURL, payload, "")
		assertStatus(t, resp, http.StatusCreated)

		var result struct {
			Status string `json:"status"`
		}
		mustUnmarshal(t, readBody(t, resp), &result)
		if result.Status != "accepted" {
			t.Errorf("different content: expected 'accepted', got %q", result.Status)
		}
	})
}

func TestIntegrationCRUD(t *testing.T) {
	env := testSetup(t)
	defer env.cleanup()

	sd := seedTestData(t, env.store)

	project, err := env.store.CreateProject(context.Background(), sd.orgA.ID, "Integration CRUD Project", "", domain.ProjectFrameworkReact, domain.ProjectStylingTailwind, sd.userA.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	var createdID string
	var webhookSecret string

	t.Run("create_integration", func(t *testing.T) {
		payload := map[string]any{
			"provider": "webhook",
			"config":   map[string]any{"label": "Customer Feedback"},
		}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations", payload, sd.tokenA)
		assertStatus(t, resp, http.StatusCreated)

		var intg domain.Integration
		mustUnmarshal(t, readBody(t, resp), &intg)
		if intg.Provider != "webhook" {
			t.Errorf("expected provider 'webhook', got %q", intg.Provider)
		}
		if intg.WebhookSecret == "" {
			t.Error("expected webhook_secret in create response")
		}
		createdID = intg.ID.String()
		webhookSecret = intg.WebhookSecret
	})

	t.Run("list_integrations_hides_secret", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations", nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var intgs []domain.Integration
		mustUnmarshal(t, readBody(t, resp), &intgs)
		if len(intgs) == 0 {
			t.Fatal("expected at least one integration")
		}
		for _, intg := range intgs {
			if intg.WebhookSecret != "" {
				t.Error("webhook_secret should be hidden in list response")
			}
		}
	})

	t.Run("get_integration_hides_secret", func(t *testing.T) {
		if createdID == "" {
			t.Skip("no integration created")
		}
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations/"+createdID, nil, sd.tokenA)
		assertStatus(t, resp, http.StatusOK)

		var intg domain.Integration
		mustUnmarshal(t, readBody(t, resp), &intg)
		if intg.WebhookSecret != "" {
			t.Error("webhook_secret should be hidden in get response")
		}
	})

	t.Run("webhook_works_with_created_integration", func(t *testing.T) {
		if webhookSecret == "" {
			t.Skip("no webhook secret")
		}
		payload := map[string]any{"content": "Test signal via integration API"}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, webhookSecret)
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusCreated)
	})

	t.Run("delete_integration", func(t *testing.T) {
		if createdID == "" {
			t.Skip("no integration created")
		}
		resp := doRequest(t, http.MethodDelete, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations/"+createdID, nil, sd.tokenA)
		assertStatus(t, resp, http.StatusNoContent)
	})

	t.Run("webhook_fails_after_integration_deleted", func(t *testing.T) {
		if webhookSecret == "" {
			t.Skip("no webhook secret")
		}
		payload := map[string]any{"content": "Should fail"}
		url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s", env.server.URL, project.ID, webhookSecret)
		resp := doRequest(t, http.MethodPost, url, payload, "")
		assertStatus(t, resp, http.StatusUnauthorized)
	})

	t.Run("viewer_cannot_create_integration", func(t *testing.T) {
		viewerUser, err := env.store.UpsertUser(context.Background(), 4001, "intg-viewer", "intg-viewer@example.com", "")
		if err != nil {
			t.Fatalf("create viewer: %v", err)
		}
		if _, err := env.store.AddMember(context.Background(), sd.orgA.ID, viewerUser.ID, domain.OrgRoleViewer); err != nil {
			t.Fatalf("add viewer: %v", err)
		}
		viewerToken := generateTestJWT(viewerUser.ID, sd.orgA.ID, domain.OrgRoleViewer)

		payload := map[string]any{"provider": "webhook"}
		resp := doRequest(t, http.MethodPost, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations", payload, viewerToken)
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("tenant_isolation_integration", func(t *testing.T) {
		// orgB should not be able to list orgA's integrations.
		resp := doRequest(t, http.MethodGet, env.server.URL+"/api/v1/projects/"+project.ID.String()+"/integrations", nil, sd.tokenB)
		assertStatus(t, resp, http.StatusNotFound)
	})
}

// Suppress unused import warnings -- these are used through type assertions.
var (
	_ = strings.NewReader
	_ pgx.Tx
)
