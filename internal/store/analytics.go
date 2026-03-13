package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ─── Analytics types ─────────────────────────────────────────────────────────

// OrgAnalytics is the top-level response for the analytics dashboard.
type OrgAnalytics struct {
	// Summary counts.
	TotalSignals    int     `json:"total_signals"`
	TotalCandidates int     `json:"total_candidates"`
	TotalPRs        int     `json:"total_prs"`
	PipelineSuccess float64 `json:"pipeline_success_rate"`

	// Time-series data.
	SignalTrend   []DailyCount `json:"signal_trend"`
	PipelineTrend []DailyCount `json:"pipeline_trend"`

	// Pipeline status breakdown.
	PipelineBreakdown []StatusCount `json:"pipeline_breakdown"`

	// Candidate status breakdown.
	CandidateBreakdown []StatusCount `json:"candidate_breakdown"`

	// Signals by source.
	SignalsBySource []SourceCount `json:"signals_by_source"`

	// Per-project breakdown.
	Projects []ProjectAnalytics `json:"projects"`

	// Team activity.
	TeamActivity []MemberActivity `json:"team_activity"`
}

// DailyCount represents a single day's count for a time-series chart.
type DailyCount struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Count int    `json:"count"`
}

// StatusCount represents a count by status.
type StatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// SourceCount represents a count by signal source.
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// ProjectAnalytics holds per-project analytics.
type ProjectAnalytics struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	SignalCount    int       `json:"signal_count"`
	CandidateCount int       `json:"candidate_count"`
	PRCount        int       `json:"pr_count"`
	PipelineCount  int       `json:"pipeline_count"`
}

// MemberActivity holds activity counts per team member.
type MemberActivity struct {
	UserID         uuid.UUID `json:"user_id"`
	DisplayName    string    `json:"display_name"`
	SignalsUploaded int      `json:"signals_uploaded"`
	SpecsGenerated  int      `json:"specs_generated"`
	PRsCreated      int      `json:"prs_created"`
}

// ─── Store methods ──────────────────────────────────────────────────────────

// GetOrgAnalytics returns comprehensive analytics for the org dashboard.
func (s *Store) GetOrgAnalytics(ctx context.Context, orgID uuid.UUID, days int) (OrgAnalytics, error) {
	since := time.Now().AddDate(0, 0, -days)
	var analytics OrgAnalytics

	// Summary counts (within the date range).
	if err := s.getOrgSummaryCounts(ctx, orgID, since, &analytics); err != nil {
		return OrgAnalytics{}, err
	}

	// Signal trend (daily counts).
	trend, err := s.getSignalTrend(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.SignalTrend = trend

	// Pipeline trend (daily counts).
	pTrend, err := s.getPipelineTrend(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.PipelineTrend = pTrend

	// Pipeline status breakdown.
	pb, err := s.getPipelineBreakdown(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.PipelineBreakdown = pb

	// Candidate status breakdown.
	cb, err := s.getCandidateBreakdown(ctx, orgID)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.CandidateBreakdown = cb

	// Signals by source.
	ss, err := s.getSignalsBySource(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.SignalsBySource = ss

	// Per-project breakdown.
	projects, err := s.getProjectBreakdown(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.Projects = projects

	// Team activity.
	team, err := s.getTeamActivity(ctx, orgID, since)
	if err != nil {
		return OrgAnalytics{}, err
	}
	analytics.TeamActivity = team

	return analytics, nil
}

func (s *Store) getOrgSummaryCounts(ctx context.Context, orgID uuid.UUID, since time.Time, a *OrgAnalytics) error {
	const q = `
		SELECT
			(SELECT COUNT(*) FROM signals s JOIN projects p ON p.id = s.project_id
			 WHERE p.org_id = $1 AND s.ingested_at >= $2)::int,
			(SELECT COUNT(*) FROM feature_candidates fc JOIN projects p ON p.id = fc.project_id
			 WHERE p.org_id = $1 AND fc.suggested_at >= $2)::int,
			(SELECT COUNT(*) FROM generations g JOIN projects p ON p.id = g.project_id
			 WHERE p.org_id = $1 AND g.pr_url IS NOT NULL AND g.pr_url != '' AND g.created_at >= $2)::int`

	err := s.pool.QueryRow(ctx, q, orgID, since).Scan(
		&a.TotalSignals, &a.TotalCandidates, &a.TotalPRs,
	)
	if err != nil {
		return fmt.Errorf("store.getOrgSummaryCounts: %w", err)
	}

	// Pipeline success rate.
	const qRate = `
		SELECT
			COUNT(*) FILTER (WHERE pr.status = 'completed')::float /
			GREATEST(COUNT(*)::float, 1)
		FROM pipeline_runs pr
		JOIN projects p ON p.id = pr.project_id
		WHERE p.org_id = $1 AND pr.created_at >= $2`

	err = s.pool.QueryRow(ctx, qRate, orgID, since).Scan(&a.PipelineSuccess)
	if err != nil {
		return fmt.Errorf("store.getOrgSummaryCounts(rate): %w", err)
	}

	return nil
}

func (s *Store) getSignalTrend(ctx context.Context, orgID uuid.UUID, since time.Time) ([]DailyCount, error) {
	const q = `
		SELECT d.date::text, COALESCE(c.cnt, 0)::int
		FROM generate_series($2::date, CURRENT_DATE, '1 day') AS d(date)
		LEFT JOIN (
			SELECT DATE(s.ingested_at) AS day, COUNT(*) AS cnt
			FROM signals s
			JOIN projects p ON p.id = s.project_id
			WHERE p.org_id = $1 AND s.ingested_at >= $2
			GROUP BY day
		) c ON c.day = d.date
		ORDER BY d.date`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getSignalTrend: %w", err)
	}
	defer rows.Close()

	var trend []DailyCount
	for rows.Next() {
		var dc DailyCount
		if err := rows.Scan(&dc.Date, &dc.Count); err != nil {
			return nil, fmt.Errorf("store.getSignalTrend: scan: %w", err)
		}
		trend = append(trend, dc)
	}
	return trend, rows.Err()
}

func (s *Store) getPipelineTrend(ctx context.Context, orgID uuid.UUID, since time.Time) ([]DailyCount, error) {
	const q = `
		SELECT d.date::text, COALESCE(c.cnt, 0)::int
		FROM generate_series($2::date, CURRENT_DATE, '1 day') AS d(date)
		LEFT JOIN (
			SELECT DATE(pr.created_at) AS day, COUNT(*) AS cnt
			FROM pipeline_runs pr
			JOIN projects p ON p.id = pr.project_id
			WHERE p.org_id = $1 AND pr.created_at >= $2
			GROUP BY day
		) c ON c.day = d.date
		ORDER BY d.date`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getPipelineTrend: %w", err)
	}
	defer rows.Close()

	var trend []DailyCount
	for rows.Next() {
		var dc DailyCount
		if err := rows.Scan(&dc.Date, &dc.Count); err != nil {
			return nil, fmt.Errorf("store.getPipelineTrend: scan: %w", err)
		}
		trend = append(trend, dc)
	}
	return trend, rows.Err()
}

func (s *Store) getPipelineBreakdown(ctx context.Context, orgID uuid.UUID, since time.Time) ([]StatusCount, error) {
	const q = `
		SELECT pr.status, COUNT(*)::int
		FROM pipeline_runs pr
		JOIN projects p ON p.id = pr.project_id
		WHERE p.org_id = $1 AND pr.created_at >= $2
		GROUP BY pr.status
		ORDER BY COUNT(*) DESC`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getPipelineBreakdown: %w", err)
	}
	defer rows.Close()

	var breakdown []StatusCount
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			return nil, fmt.Errorf("store.getPipelineBreakdown: scan: %w", err)
		}
		breakdown = append(breakdown, sc)
	}
	return breakdown, rows.Err()
}

func (s *Store) getCandidateBreakdown(ctx context.Context, orgID uuid.UUID) ([]StatusCount, error) {
	const q = `
		SELECT fc.status, COUNT(*)::int
		FROM feature_candidates fc
		JOIN projects p ON p.id = fc.project_id
		WHERE p.org_id = $1
		GROUP BY fc.status
		ORDER BY COUNT(*) DESC`

	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("store.getCandidateBreakdown: %w", err)
	}
	defer rows.Close()

	var breakdown []StatusCount
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			return nil, fmt.Errorf("store.getCandidateBreakdown: scan: %w", err)
		}
		breakdown = append(breakdown, sc)
	}
	return breakdown, rows.Err()
}

func (s *Store) getSignalsBySource(ctx context.Context, orgID uuid.UUID, since time.Time) ([]SourceCount, error) {
	const q = `
		SELECT s.source, COUNT(*)::int
		FROM signals s
		JOIN projects p ON p.id = s.project_id
		WHERE p.org_id = $1 AND s.ingested_at >= $2
		GROUP BY s.source
		ORDER BY COUNT(*) DESC`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getSignalsBySource: %w", err)
	}
	defer rows.Close()

	var sources []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.Count); err != nil {
			return nil, fmt.Errorf("store.getSignalsBySource: scan: %w", err)
		}
		sources = append(sources, sc)
	}
	return sources, rows.Err()
}

func (s *Store) getProjectBreakdown(ctx context.Context, orgID uuid.UUID, since time.Time) ([]ProjectAnalytics, error) {
	const q = `
		SELECT
			p.id, p.name,
			COALESCE(sig.cnt, 0)::int,
			COALESCE(fc.cnt, 0)::int,
			COALESCE(gen.cnt, 0)::int,
			COALESCE(pr.cnt, 0)::int
		FROM projects p
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM signals
			WHERE ingested_at >= $2 GROUP BY project_id
		) sig ON sig.project_id = p.id
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM feature_candidates
			GROUP BY project_id
		) fc ON fc.project_id = p.id
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM generations
			WHERE pr_url IS NOT NULL AND pr_url != '' AND created_at >= $2
			GROUP BY project_id
		) gen ON gen.project_id = p.id
		LEFT JOIN (
			SELECT project_id, COUNT(*) AS cnt FROM pipeline_runs
			WHERE created_at >= $2 GROUP BY project_id
		) pr ON pr.project_id = p.id
		WHERE p.org_id = $1
		ORDER BY p.name`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getProjectBreakdown: %w", err)
	}
	defer rows.Close()

	var projects []ProjectAnalytics
	for rows.Next() {
		var pa ProjectAnalytics
		if err := rows.Scan(
			&pa.ID, &pa.Name, &pa.SignalCount, &pa.CandidateCount, &pa.PRCount, &pa.PipelineCount,
		); err != nil {
			return nil, fmt.Errorf("store.getProjectBreakdown: scan: %w", err)
		}
		projects = append(projects, pa)
	}
	return projects, rows.Err()
}

func (s *Store) getTeamActivity(ctx context.Context, orgID uuid.UUID, since time.Time) ([]MemberActivity, error) {
	const q = `
		SELECT
			u.id,
			COALESCE(u.name, u.github_login, 'Unknown') AS display_name,
			COALESCE(sig.cnt, 0)::int AS signals_uploaded,
			COALESCE(sp.cnt, 0)::int AS specs_generated,
			COALESCE(gen.cnt, 0)::int AS prs_created
		FROM users u
		JOIN org_members om ON om.user_id = u.id
		LEFT JOIN (
			SELECT p.created_by, COUNT(*) AS cnt
			FROM signals s JOIN projects p ON p.id = s.project_id
			WHERE p.org_id = $1 AND s.ingested_at >= $2
			GROUP BY p.created_by
		) sig ON sig.created_by = u.id
		LEFT JOIN (
			SELECT p.created_by, COUNT(*) AS cnt
			FROM specs sp2 JOIN projects p ON p.id = sp2.project_id
			WHERE p.org_id = $1 AND sp2.created_at >= $2
			GROUP BY p.created_by
		) sp ON sp.created_by = u.id
		LEFT JOIN (
			SELECT p.created_by, COUNT(*) AS cnt
			FROM generations g JOIN projects p ON p.id = g.project_id
			WHERE p.org_id = $1 AND g.pr_url IS NOT NULL AND g.pr_url != '' AND g.created_at >= $2
			GROUP BY p.created_by
		) gen ON gen.created_by = u.id
		WHERE om.org_id = $1
		ORDER BY display_name`

	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return nil, fmt.Errorf("store.getTeamActivity: %w", err)
	}
	defer rows.Close()

	var members []MemberActivity
	for rows.Next() {
		var m MemberActivity
		if err := rows.Scan(
			&m.UserID, &m.DisplayName, &m.SignalsUploaded, &m.SpecsGenerated, &m.PRsCreated,
		); err != nil {
			return nil, fmt.Errorf("store.getTeamActivity: scan: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
