package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/neuco-ai/neuco/internal/domain"
)

// Internal query methods used by workers. These skip tenant scoping because
// workers operate on job arguments that already include verified project IDs.
// These should NEVER be exposed through HTTP handlers.

// GetSpecInternal fetches a spec by ID without project scoping (for workers).
func (s *Store) GetSpecInternal(ctx context.Context, specID uuid.UUID) (*domain.Spec, error) {
	const q = `
		SELECT id, candidate_id, project_id, version,
		       problem_statement, proposed_solution,
		       user_stories, acceptance_criteria, out_of_scope,
		       ui_changes, data_model_changes, open_questions,
		       created_at
		FROM   specs
		WHERE  id = $1
		ORDER  BY version DESC
		LIMIT  1`

	var spec domain.Spec
	var userStoriesJSON, criteriaJSON, oosJSON, oqJSON []byte
	err := s.pool.QueryRow(ctx, q, specID).Scan(
		&spec.ID,
		&spec.CandidateID,
		&spec.ProjectID,
		&spec.Version,
		&spec.ProblemStatement,
		&spec.ProposedSolution,
		&userStoriesJSON,
		&criteriaJSON,
		&oosJSON,
		&spec.UIChanges,
		&spec.DataModelChanges,
		&oqJSON,
		&spec.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetSpecInternal: %w", err)
	}
	json.Unmarshal(userStoriesJSON, &spec.UserStories)
	json.Unmarshal(criteriaJSON, &spec.AcceptanceCriteria)
	json.Unmarshal(oosJSON, &spec.OutOfScope)
	json.Unmarshal(oqJSON, &spec.OpenQuestions)
	return &spec, nil
}

// GetCandidateInternal fetches a candidate by ID without project scoping.
func (s *Store) GetCandidateInternal(ctx context.Context, candidateID uuid.UUID) (*domain.FeatureCandidate, error) {
	const q = `
		SELECT id, project_id, title, problem_summary, signal_count, score, status, created_at
		FROM   feature_candidates
		WHERE  id = $1`

	var c domain.FeatureCandidate
	err := s.pool.QueryRow(ctx, q, candidateID).Scan(
		&c.ID, &c.ProjectID, &c.Title, &c.ProblemSummary,
		&c.SignalCount, &c.Score, &c.Status, &c.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetCandidateInternal: %w", err)
	}
	return &c, nil
}

// GetProjectInternal fetches a project by ID without org scoping.
func (s *Store) GetProjectInternal(ctx context.Context, projectID uuid.UUID) (*domain.Project, error) {
	const q = `
		SELECT id, org_id, name, github_repo, framework, styling, created_by, created_at
		FROM   projects
		WHERE  id = $1`

	var p domain.Project
	err := s.pool.QueryRow(ctx, q, projectID).Scan(
		&p.ID, &p.OrgID, &p.Name, &p.GitHubRepo,
		&p.Framework, &p.Styling, &p.CreatedBy, &p.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetProjectInternal: %w", err)
	}
	return &p, nil
}

// GetSignalInternal fetches a signal by ID without project scoping.
func (s *Store) GetSignalInternal(ctx context.Context, signalID uuid.UUID) (*domain.Signal, error) {
	const q = `
		SELECT id, project_id, source, source_ref, type, content, metadata, occurred_at, ingested_at
		FROM   signals
		WHERE  id = $1`

	var sig domain.Signal
	err := s.pool.QueryRow(ctx, q, signalID).Scan(
		&sig.ID, &sig.ProjectID, &sig.Source, &sig.SourceRef,
		&sig.Type, &sig.Content, &sig.Metadata,
		&sig.OccurredAt, &sig.IngestedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetSignalInternal: %w", err)
	}
	return &sig, nil
}

// GetCandidateSignals fetches signals linked to a candidate, ordered by relevance.
func (s *Store) GetCandidateSignals(ctx context.Context, candidateID uuid.UUID, limit int) ([]domain.Signal, error) {
	const q = `
		SELECT s.id, s.project_id, s.source, s.source_ref, s.type, s.content, s.metadata,
		       s.occurred_at, s.ingested_at
		FROM   signals s
		JOIN   candidate_signals cs ON cs.signal_id = s.id
		WHERE  cs.candidate_id = $1
		ORDER  BY cs.relevance DESC
		LIMIT  $2`

	rows, err := s.pool.Query(ctx, q, candidateID, limit)
	if err != nil {
		return nil, fmt.Errorf("store.GetCandidateSignals: %w", err)
	}
	defer rows.Close()

	var signals []domain.Signal
	for rows.Next() {
		var sig domain.Signal
		if err := rows.Scan(
			&sig.ID, &sig.ProjectID, &sig.Source, &sig.SourceRef,
			&sig.Type, &sig.Content, &sig.Metadata,
			&sig.OccurredAt, &sig.IngestedAt,
		); err != nil {
			return nil, fmt.Errorf("store.GetCandidateSignals: scan: %w", err)
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

// LinkCandidateSignal creates a link between a candidate and a signal.
func (s *Store) LinkCandidateSignal(ctx context.Context, candidateID, signalID uuid.UUID, relevance float64) error {
	const q = `
		INSERT INTO candidate_signals (candidate_id, signal_id, relevance)
		VALUES ($1, $2, $3)
		ON CONFLICT (candidate_id, signal_id) DO UPDATE SET relevance = $3`

	_, err := s.pool.Exec(ctx, q, candidateID, signalID, relevance)
	if err != nil {
		return fmt.Errorf("store.LinkCandidateSignal: %w", err)
	}
	return nil
}

// UpdateCandidateTheme updates the title and problem summary of a candidate.
func (s *Store) UpdateCandidateTheme(ctx context.Context, candidateID uuid.UUID, title, summary string) error {
	const q = `
		UPDATE feature_candidates
		SET    title = $2, problem_summary = $3
		WHERE  id = $1`

	_, err := s.pool.Exec(ctx, q, candidateID, title, summary)
	if err != nil {
		return fmt.Errorf("store.UpdateCandidateTheme: %w", err)
	}
	return nil
}

// UpdateCandidateScore updates the score of a candidate.
func (s *Store) UpdateCandidateScore(ctx context.Context, candidateID uuid.UUID, score float64) error {
	const q = `UPDATE feature_candidates SET score = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, candidateID, score)
	if err != nil {
		return fmt.Errorf("store.UpdateCandidateScore: %w", err)
	}
	return nil
}

// ListEmbeddedSignals returns signals that have embeddings for a project.
func (s *Store) ListEmbeddedSignals(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.Signal, error) {
	const q = `
		SELECT id, project_id, source, source_ref, type, content, metadata,
		       occurred_at, ingested_at, embedding
		FROM   signals
		WHERE  project_id = $1 AND embedding IS NOT NULL
		ORDER  BY ingested_at DESC
		LIMIT  $2`

	rows, err := s.pool.Query(ctx, q, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("store.ListEmbeddedSignals: %w", err)
	}
	defer rows.Close()

	var signals []domain.Signal
	for rows.Next() {
		var sig domain.Signal
		if err := rows.Scan(
			&sig.ID, &sig.ProjectID, &sig.Source, &sig.SourceRef,
			&sig.Type, &sig.Content, &sig.Metadata,
			&sig.OccurredAt, &sig.IngestedAt, &sig.Embedding,
		); err != nil {
			return nil, fmt.Errorf("store.ListEmbeddedSignals: scan: %w", err)
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

// ListAllActiveProjects returns all projects (used by weekly digest cron).
func (s *Store) ListAllActiveProjects(ctx context.Context) ([]domain.Project, error) {
	const q = `
		SELECT id, org_id, name, github_repo, framework, styling, created_by, created_at
		FROM   projects
		ORDER  BY created_at`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store.ListAllActiveProjects: %w", err)
	}
	defer rows.Close()

	var projects []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(
			&p.ID, &p.OrgID, &p.Name, &p.GitHubRepo,
			&p.Framework, &p.Styling, &p.CreatedBy, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store.ListAllActiveProjects: scan: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetIntegrationInternal fetches an integration by ID without tenant scoping
// (for workers that need to look up last_sync_at).
func (s *Store) GetIntegrationInternal(ctx context.Context, integrationID uuid.UUID) (domain.Integration, error) {
	const q = `SELECT ` + integrationColumns + ` FROM integrations WHERE id = $1`
	row := s.pool.QueryRow(ctx, q, integrationID)
	intg, err := scanIntegration(row)
	if err != nil {
		return domain.Integration{}, fmt.Errorf("store.GetIntegrationInternal: %w", err)
	}
	return intg, nil
}

// ListActiveIntegrationsInternal returns all active integrations for a given
// provider (e.g. "gong", "intercom"). Used by the periodic sync worker.
func (s *Store) ListActiveIntegrationsInternal(ctx context.Context, provider string) ([]domain.Integration, error) {
	const q = `SELECT ` + integrationColumns + ` FROM integrations WHERE provider = $1 AND is_active = true ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, provider)
	if err != nil {
		return nil, fmt.Errorf("store.ListActiveIntegrationsInternal: %w", err)
	}
	defer rows.Close()

	var intgs []domain.Integration
	for rows.Next() {
		intg, err := scanIntegration(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListActiveIntegrationsInternal: scan: %w", err)
		}
		intgs = append(intgs, intg)
	}
	return intgs, rows.Err()
}

// ListAllActiveIntegrationsInternal returns all active non-webhook integrations.
// Used by the periodic sync worker to trigger syncs across all providers.
func (s *Store) ListAllActiveIntegrationsInternal(ctx context.Context) ([]domain.Integration, error) {
	const q = `SELECT ` + integrationColumns + ` FROM integrations WHERE is_active = true AND provider != 'webhook' ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store.ListAllActiveIntegrationsInternal: %w", err)
	}
	defer rows.Close()

	var intgs []domain.Integration
	for rows.Next() {
		intg, err := scanIntegration(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListAllActiveIntegrationsInternal: scan: %w", err)
		}
		intgs = append(intgs, intg)
	}
	return intgs, rows.Err()
}

// GetProjectStats returns aggregate counters for the project dashboard.
func (s *Store) GetProjectStats(ctx context.Context, projectID uuid.UUID) (ProjectStats, error) {
	const q = `
		SELECT
			(SELECT COUNT(*) FROM signals           WHERE project_id = $1)::int AS signal_count,
			(SELECT COUNT(*) FROM feature_candidates WHERE project_id = $1)::int AS candidate_count,
			(SELECT COUNT(*) FROM generations        WHERE project_id = $1)::int AS generation_count,
			(SELECT COUNT(*) FROM pipeline_runs      WHERE project_id = $1)::int AS pipeline_count`

	var stats ProjectStats
	err := s.pool.QueryRow(ctx, q, projectID).Scan(
		&stats.SignalCount,
		&stats.CandidateCount,
		&stats.GenerationCount,
		&stats.PipelineCount,
	)
	if err != nil {
		return ProjectStats{}, fmt.Errorf("store.GetProjectStats: %w", err)
	}
	return stats, nil
}

// OrgWeeklyStats holds aggregate counts for the past 7 days for a single org.
type OrgWeeklyStats struct {
	SignalCount    int
	CandidateCount int
	SpecCount      int
	PRCount        int
}

// ProjectWeeklyStats holds per-project stats for the past 7 days.
type ProjectWeeklyStats struct {
	ProjectName string
	SignalCount int
	PRCount     int
}

// GetOrgWeeklyStats returns aggregate counts for the past 7 days across all projects in an org.
func (s *Store) GetOrgWeeklyStats(ctx context.Context, orgID uuid.UUID) (OrgWeeklyStats, error) {
	const q = `
		SELECT
			(SELECT COUNT(*) FROM signals s JOIN projects p ON p.id = s.project_id WHERE p.org_id = $1 AND s.ingested_at > NOW() - INTERVAL '7 days')::int,
			(SELECT COUNT(*) FROM feature_candidates fc JOIN projects p ON p.id = fc.project_id WHERE p.org_id = $1 AND fc.suggested_at > NOW() - INTERVAL '7 days')::int,
			(SELECT COUNT(*) FROM specs sp JOIN projects p ON p.id = sp.project_id WHERE p.org_id = $1 AND sp.created_at > NOW() - INTERVAL '7 days')::int,
			(SELECT COUNT(*) FROM generations g JOIN projects p ON p.id = g.project_id WHERE p.org_id = $1 AND g.pr_url IS NOT NULL AND g.pr_url != '' AND g.created_at > NOW() - INTERVAL '7 days')::int`

	var stats OrgWeeklyStats
	err := s.pool.QueryRow(ctx, q, orgID).Scan(
		&stats.SignalCount, &stats.CandidateCount, &stats.SpecCount, &stats.PRCount,
	)
	if err != nil {
		return OrgWeeklyStats{}, fmt.Errorf("store.GetOrgWeeklyStats: %w", err)
	}
	return stats, nil
}

// GetProjectWeeklyStats returns per-project signal and PR counts for the past 7 days.
func (s *Store) GetProjectWeeklyStats(ctx context.Context, orgID uuid.UUID) ([]ProjectWeeklyStats, error) {
	const q = `
		SELECT p.name,
			COALESCE(sig.cnt, 0)::int,
			COALESCE(gen.cnt, 0)::int
		FROM projects p
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM signals
			WHERE ingested_at > NOW() - INTERVAL '7 days'
			GROUP BY project_id
		) sig ON sig.project_id = p.id
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM generations
			WHERE pr_url IS NOT NULL AND pr_url != '' AND created_at > NOW() - INTERVAL '7 days'
			GROUP BY project_id
		) gen ON gen.project_id = p.id
		WHERE p.org_id = $1
		ORDER BY p.name`

	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("store.GetProjectWeeklyStats: %w", err)
	}
	defer rows.Close()

	var stats []ProjectWeeklyStats
	for rows.Next() {
		var ps ProjectWeeklyStats
		if err := rows.Scan(&ps.ProjectName, &ps.SignalCount, &ps.PRCount); err != nil {
			return nil, fmt.Errorf("store.GetProjectWeeklyStats: scan: %w", err)
		}
		stats = append(stats, ps)
	}
	return stats, rows.Err()
}

// GetOrgTopCopilotInsights returns the top N non-dismissed copilot notes
// created in the past 7 days across all projects in an org, ordered by recency.
func (s *Store) GetOrgTopCopilotInsights(ctx context.Context, orgID uuid.UUID, limit int) ([]OrgCopilotInsight, error) {
	const q = `
		SELECT cn.content, cn.note_type, p.name AS project_name
		FROM   copilot_notes cn
		JOIN   projects p ON p.id = cn.project_id
		WHERE  p.org_id = $1
		  AND  cn.dismissed = FALSE
		  AND  cn.created_at > NOW() - INTERVAL '7 days'
		ORDER  BY cn.created_at DESC
		LIMIT  $2`

	rows, err := s.pool.Query(ctx, q, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("store.GetOrgTopCopilotInsights: %w", err)
	}
	defer rows.Close()

	var insights []OrgCopilotInsight
	for rows.Next() {
		var i OrgCopilotInsight
		if err := rows.Scan(&i.Content, &i.NoteType, &i.ProjectName); err != nil {
			return nil, fmt.Errorf("store.GetOrgTopCopilotInsights: scan: %w", err)
		}
		insights = append(insights, i)
	}
	return insights, rows.Err()
}

// OrgCopilotInsight is a copilot note with its parent project name for digest emails.
type OrgCopilotInsight struct {
	Content     string
	NoteType    string
	ProjectName string
}

// UpdatePipelineRunError updates the error field of a pipeline run.
func (s *Store) UpdatePipelineRunError(ctx context.Context, runID uuid.UUID, errMsg string) error {
	const q = `UPDATE pipeline_runs SET error = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, runID, errMsg)
	return err
}

// UpdatePipelineRunMetadata replaces the metadata JSONB column of a pipeline
// run. Workers use this to pass structured data (e.g. a repo index) to
// downstream workers without an additional database table.
// metadata must be a valid JSON object (json.RawMessage).
func (s *Store) UpdatePipelineRunMetadata(ctx context.Context, runID uuid.UUID, metadata json.RawMessage) error {
	const q = `UPDATE pipeline_runs SET metadata = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, runID, metadata)
	if err != nil {
		return fmt.Errorf("store.UpdatePipelineRunMetadata: %w", err)
	}
	return nil
}
