package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const sandboxSessionColumns = `
	id, generation_id, project_id, org_id,
	agent_provider, agent_model,
	sandbox_provider, sandbox_external_id,
	status, agent_log,
	files_changed, test_results, validation_results,
	tokens_used, cost_usd,
	retry_count, max_retries,
	error_message,
	started_at, completed_at, created_at`

// SandboxSessionRow maps to sandbox_sessions.
type SandboxSessionRow struct {
	ID                uuid.UUID       `db:"id"`
	GenerationID      uuid.UUID       `db:"generation_id"`
	ProjectID         uuid.UUID       `db:"project_id"`
	OrgID             uuid.UUID       `db:"org_id"`
	AgentProvider     string          `db:"agent_provider"`
	AgentModel        *string         `db:"agent_model"`
	SandboxProvider   string          `db:"sandbox_provider"`
	SandboxExternalID *string         `db:"sandbox_external_id"`
	Status            string          `db:"status"`
	AgentLog          *string         `db:"agent_log"`
	FilesChanged      json.RawMessage `db:"files_changed"`
	TestResults       json.RawMessage `db:"test_results"`
	ValidationResults json.RawMessage `db:"validation_results"`
	TokensUsed        *int            `db:"tokens_used"`
	CostUSD           *float64        `db:"cost_usd"`
	RetryCount        int             `db:"retry_count"`
	MaxRetries        int             `db:"max_retries"`
	ErrorMessage      *string         `db:"error_message"`
	StartedAt         *time.Time      `db:"started_at"`
	CompletedAt       *time.Time      `db:"completed_at"`
	CreatedAt         time.Time       `db:"created_at"`
}

// SandboxSessionResult carries completion data for a session run.
type SandboxSessionResult struct {
	AgentLog          *string
	FilesChanged      json.RawMessage
	TestResults       json.RawMessage
	ValidationResults json.RawMessage
	TokensUsed        *int
	CostUSD           *float64
	RetryCount        *int
	ErrorMessage      *string
	StartedAt         *time.Time
	CompletedAt       *time.Time
}

// CreateSandboxSession inserts a new sandbox session and returns the stored row.
func (s *Store) CreateSandboxSession(ctx context.Context, session SandboxSessionRow) (SandboxSessionRow, error) {
	if session.ID == uuid.Nil {
		session.ID = uuid.New()
	}
	filesChanged, testResults, validationResults := normalizeSessionJSON(session.FilesChanged, session.TestResults, session.ValidationResults)

	const q = `
		INSERT INTO sandbox_sessions (
			id, generation_id, project_id, org_id,
			agent_provider, agent_model,
			sandbox_provider, sandbox_external_id,
			status, agent_log,
			files_changed, test_results, validation_results,
			tokens_used, cost_usd,
			retry_count, max_retries,
			error_message,
			started_at, completed_at
		)
		VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8,
			$9, $10,
			$11, $12, $13,
			$14, $15,
			$16, $17,
			$18,
			$19, $20
		)
		RETURNING ` + sandboxSessionColumns

	row := s.pool.QueryRow(ctx, q,
		session.ID,
		session.GenerationID,
		session.ProjectID,
		session.OrgID,
		session.AgentProvider,
		session.AgentModel,
		session.SandboxProvider,
		session.SandboxExternalID,
		session.Status,
		session.AgentLog,
		filesChanged,
		testResults,
		validationResults,
		session.TokensUsed,
		session.CostUSD,
		session.RetryCount,
		session.MaxRetries,
		session.ErrorMessage,
		session.StartedAt,
		session.CompletedAt,
	)

	out, err := scanSandboxSession(row)
	if err != nil {
		return SandboxSessionRow{}, fmt.Errorf("store.CreateSandboxSession: %w", err)
	}
	return out, nil
}

// GetSandboxSession returns one session by ID.
func (s *Store) GetSandboxSession(ctx context.Context, sessionID uuid.UUID) (*SandboxSessionRow, error) {
	const q = `
		SELECT ` + sandboxSessionColumns + `
		FROM   sandbox_sessions
		WHERE  id = $1`

	row := s.pool.QueryRow(ctx, q, sessionID)
	session, err := scanSandboxSession(row)
	if err != nil {
		return nil, fmt.Errorf("store.GetSandboxSession: %w", err)
	}
	return &session, nil
}

// UpdateSandboxSessionStatus updates only status and error_message (and timestamps for terminal/running states).
func (s *Store) UpdateSandboxSessionStatus(ctx context.Context, sessionID uuid.UUID, status string, errorMsg *string) error {
	const q = `
		UPDATE sandbox_sessions
		SET    status = $2,
		       error_message = $3,
		       started_at = CASE
		            WHEN $2 IN ('running','validating') AND started_at IS NULL THEN NOW()
		            ELSE started_at
		       END,
		       completed_at = CASE
		            WHEN $2 IN ('completed','failed','cancelled','timed_out') THEN NOW()
		            ELSE completed_at
		       END
		WHERE  id = $1`

	_, err := s.pool.Exec(ctx, q, sessionID, status, errorMsg)
	if err != nil {
		return fmt.Errorf("store.UpdateSandboxSessionStatus: %w", err)
	}
	return nil
}

// UpdateSandboxSessionResult stores final run artifacts and metrics.
func (s *Store) UpdateSandboxSessionResult(ctx context.Context, sessionID uuid.UUID, result SandboxSessionResult) error {
	filesChanged, testResults, validationResults := normalizeSessionJSON(result.FilesChanged, result.TestResults, result.ValidationResults)

	const q = `
		UPDATE sandbox_sessions
		SET    agent_log          = COALESCE($2, agent_log),
		       files_changed      = $3,
		       test_results       = $4,
		       validation_results = $5,
		       tokens_used        = COALESCE($6, tokens_used),
		       cost_usd           = COALESCE($7, cost_usd),
		       retry_count        = COALESCE($8, retry_count),
		       error_message      = $9,
		       started_at         = COALESCE($10, started_at),
		       completed_at       = COALESCE($11, completed_at)
		WHERE  id = $1`

	_, err := s.pool.Exec(ctx, q,
		sessionID,
		result.AgentLog,
		filesChanged,
		testResults,
		validationResults,
		result.TokensUsed,
		result.CostUSD,
		result.RetryCount,
		result.ErrorMessage,
		result.StartedAt,
		result.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("store.UpdateSandboxSessionResult: %w", err)
	}
	return nil
}

// ListProjectSessions lists sessions for a project with pagination.
func (s *Store) ListProjectSessions(ctx context.Context, projectID uuid.UUID, page PageParams) ([]SandboxSessionRow, int, error) {
	const countQ = `SELECT COUNT(*) FROM sandbox_sessions WHERE project_id = $1`
	var total int
	if err := s.pool.QueryRow(ctx, countQ, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectSessions count: %w", err)
	}

	const q = `
		SELECT ` + sandboxSessionColumns + `
		FROM   sandbox_sessions
		WHERE  project_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, projectID, page.Limit, page.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectSessions: %w", err)
	}
	defer rows.Close()

	var sessions []SandboxSessionRow
	for rows.Next() {
		session, err := scanSandboxSession(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store.ListProjectSessions: scan: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectSessions: rows: %w", err)
	}
	return sessions, total, nil
}

func scanSandboxSession(row pgx.Row) (SandboxSessionRow, error) {
	var session SandboxSessionRow
	var filesChanged []byte
	var testResults []byte
	var validationResults []byte

	err := row.Scan(
		&session.ID,
		&session.GenerationID,
		&session.ProjectID,
		&session.OrgID,
		&session.AgentProvider,
		&session.AgentModel,
		&session.SandboxProvider,
		&session.SandboxExternalID,
		&session.Status,
		&session.AgentLog,
		&filesChanged,
		&testResults,
		&validationResults,
		&session.TokensUsed,
		&session.CostUSD,
		&session.RetryCount,
		&session.MaxRetries,
		&session.ErrorMessage,
		&session.StartedAt,
		&session.CompletedAt,
		&session.CreatedAt,
	)
	if err != nil {
		return SandboxSessionRow{}, err
	}

	session.FilesChanged = defaultJSONArray(filesChanged)
	session.TestResults = defaultJSONObject(testResults)
	session.ValidationResults = defaultJSONObject(validationResults)

	return session, nil
}

func normalizeSessionJSON(filesChanged, testResults, validationResults json.RawMessage) (json.RawMessage, json.RawMessage, json.RawMessage) {
	if filesChanged == nil {
		filesChanged = json.RawMessage(`[]`)
	}
	if testResults == nil {
		testResults = json.RawMessage(`{}`)
	}
	if validationResults == nil {
		validationResults = json.RawMessage(`{}`)
	}
	return filesChanged, testResults, validationResults
}

func defaultJSONArray(v []byte) json.RawMessage {
	if v == nil {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(v)
}

func defaultJSONObject(v []byte) json.RawMessage {
	if v == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(v)
}
