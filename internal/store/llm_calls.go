package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

const llmCallColumns = `
	id, project_id, pipeline_run_id, pipeline_task_id,
	provider, model, call_type,
	tokens_in, tokens_out, latency_ms, cost_usd,
	error_msg, created_at`

// CreateLLMCall records a single LLM API call.
func (s *Store) CreateLLMCall(ctx context.Context, call *domain.LLMCall) error {
	const q = `
		INSERT INTO llm_calls (id, project_id, pipeline_run_id, pipeline_task_id,
			provider, model, call_type,
			tokens_in, tokens_out, latency_ms, cost_usd, error_msg)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	if call.ID == uuid.Nil {
		call.ID = uuid.New()
	}

	var errMsg *string
	if call.ErrorMsg != "" {
		errMsg = &call.ErrorMsg
	}

	_, err := s.pool.Exec(ctx, q,
		call.ID,
		call.ProjectID,
		call.PipelineRunID,
		call.PipelineTaskID,
		call.Provider,
		call.Model,
		call.CallType,
		call.TokensIn,
		call.TokensOut,
		call.LatencyMs,
		call.CostUSD,
		errMsg,
	)
	if err != nil {
		return fmt.Errorf("store.CreateLLMCall: %w", err)
	}
	return nil
}

// GetLLMCallsByPipelineRun returns all LLM calls for a pipeline run.
func (s *Store) GetLLMCallsByPipelineRun(ctx context.Context, runID uuid.UUID) ([]domain.LLMCall, error) {
	q := "SELECT " + llmCallColumns + " FROM llm_calls WHERE pipeline_run_id = $1 ORDER BY created_at"

	rows, err := s.pool.Query(ctx, q, runID)
	if err != nil {
		return nil, fmt.Errorf("store.GetLLMCallsByPipelineRun: %w", err)
	}
	defer rows.Close()

	var calls []domain.LLMCall
	for rows.Next() {
		call, err := scanLLMCall(rows)
		if err != nil {
			return nil, fmt.Errorf("store.GetLLMCallsByPipelineRun: scan: %w", err)
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

// GetLLMUsageByPipelineRun returns aggregated LLM usage for a pipeline run.
func (s *Store) GetLLMUsageByPipelineRun(ctx context.Context, runID uuid.UUID) (*domain.LLMUsageAgg, error) {
	const q = `
		SELECT
			COUNT(*)::int,
			COALESCE(SUM(tokens_in), 0)::int,
			COALESCE(SUM(tokens_out), 0)::int,
			COALESCE(SUM(cost_usd), 0)::numeric,
			COALESCE(AVG(latency_ms), 0)::numeric,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::numeric
		FROM llm_calls
		WHERE pipeline_run_id = $1`

	var agg domain.LLMUsageAgg
	err := s.pool.QueryRow(ctx, q, runID).Scan(
		&agg.TotalCalls,
		&agg.TotalTokensIn,
		&agg.TotalTokensOut,
		&agg.TotalCostUSD,
		&agg.AvgLatencyMs,
		&agg.P95LatencyMs,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetLLMUsageByPipelineRun: %w", err)
	}
	return &agg, nil
}

// GetLLMUsageByProject returns aggregated LLM usage for a project over all time.
func (s *Store) GetLLMUsageByProject(ctx context.Context, projectID uuid.UUID) (*domain.LLMUsageAgg, error) {
	const q = `
		SELECT
			COUNT(*)::int,
			COALESCE(SUM(tokens_in), 0)::int,
			COALESCE(SUM(tokens_out), 0)::int,
			COALESCE(SUM(cost_usd), 0)::numeric,
			COALESCE(AVG(latency_ms), 0)::numeric,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::numeric
		FROM llm_calls
		WHERE project_id = $1`

	var agg domain.LLMUsageAgg
	err := s.pool.QueryRow(ctx, q, projectID).Scan(
		&agg.TotalCalls,
		&agg.TotalTokensIn,
		&agg.TotalTokensOut,
		&agg.TotalCostUSD,
		&agg.AvgLatencyMs,
		&agg.P95LatencyMs,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetLLMUsageByProject: %w", err)
	}
	return &agg, nil
}

// ListLLMCallsByProject returns recent LLM calls for a project with pagination.
func (s *Store) ListLLMCallsByProject(ctx context.Context, projectID uuid.UUID, pp PageParams) ([]domain.LLMCall, int, error) {
	const countQ = `SELECT COUNT(*) FROM llm_calls WHERE project_id = $1`
	var total int
	if err := s.pool.QueryRow(ctx, countQ, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store.ListLLMCallsByProject count: %w", err)
	}

	q := "SELECT " + llmCallColumns + ` FROM llm_calls WHERE project_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, projectID, pp.Limit, pp.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListLLMCallsByProject: %w", err)
	}
	defer rows.Close()

	var calls []domain.LLMCall
	for rows.Next() {
		call, err := scanLLMCall(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store.ListLLMCallsByProject: scan: %w", err)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store.ListLLMCallsByProject: rows: %w", err)
	}
	return calls, total, nil
}

// GetLLMUsageByOrg returns aggregated LLM usage for all projects in an org.
func (s *Store) GetLLMUsageByOrg(ctx context.Context, orgID uuid.UUID) (*domain.LLMUsageAgg, error) {
	const q = `
		SELECT
			COUNT(*)::int,
			COALESCE(SUM(lc.tokens_in), 0)::int,
			COALESCE(SUM(lc.tokens_out), 0)::int,
			COALESCE(SUM(lc.cost_usd), 0)::numeric,
			COALESCE(AVG(lc.latency_ms), 0)::numeric,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY lc.latency_ms), 0)::numeric
		FROM llm_calls lc
		JOIN projects p ON p.id = lc.project_id
		WHERE p.org_id = $1`

	var agg domain.LLMUsageAgg
	err := s.pool.QueryRow(ctx, q, orgID).Scan(
		&agg.TotalCalls,
		&agg.TotalTokensIn,
		&agg.TotalTokensOut,
		&agg.TotalCostUSD,
		&agg.AvgLatencyMs,
		&agg.P95LatencyMs,
	)
	if err != nil {
		return nil, fmt.Errorf("store.GetLLMUsageByOrg: %w", err)
	}
	return &agg, nil
}

func scanLLMCall(row pgx.Row) (domain.LLMCall, error) {
	var c domain.LLMCall
	var errMsg *string
	err := row.Scan(
		&c.ID,
		&c.ProjectID,
		&c.PipelineRunID,
		&c.PipelineTaskID,
		&c.Provider,
		&c.Model,
		&c.CallType,
		&c.TokensIn,
		&c.TokensOut,
		&c.LatencyMs,
		&c.CostUSD,
		&errMsg,
		&c.CreatedAt,
	)
	if errMsg != nil {
		c.ErrorMsg = *errMsg
	}
	return c, err
}
