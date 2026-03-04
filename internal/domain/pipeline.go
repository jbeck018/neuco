package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PipelineRunStatus represents the overall state of a pipeline run.
type PipelineRunStatus string

const (
	PipelineRunStatusPending   PipelineRunStatus = "pending"
	PipelineRunStatusRunning   PipelineRunStatus = "running"
	PipelineRunStatusCompleted PipelineRunStatus = "completed"
	PipelineRunStatusFailed    PipelineRunStatus = "failed"
)

// PipelineTaskStatus represents the state of a single task within a pipeline.
type PipelineTaskStatus string

const (
	PipelineTaskStatusPending   PipelineTaskStatus = "pending"
	PipelineTaskStatusRunning   PipelineTaskStatus = "running"
	PipelineTaskStatusCompleted PipelineTaskStatus = "completed"
	PipelineTaskStatusFailed    PipelineTaskStatus = "failed"
)

// PipelineType distinguishes the type of workflow the run represents.
type PipelineType string

const (
	PipelineTypeIngest    PipelineType = "ingest"
	PipelineTypeSynthesis PipelineType = "synthesis"
	PipelineTypeSpecGen   PipelineType = "spec_gen"
	PipelineTypeCodegen   PipelineType = "codegen"
	PipelineTypeDigest    PipelineType = "digest"
	PipelineTypeCopilot   PipelineType = "copilot"
	PipelineTypeNangoSync PipelineType = "nango_sync"
)

// PipelineRun is the top-level record for a workflow execution.
type PipelineRun struct {
	ID          uuid.UUID         `json:"id"`
	ProjectID   uuid.UUID         `json:"project_id"`
	Type        PipelineType      `json:"type"`
	Status      PipelineRunStatus `json:"status"`
	Metadata    json.RawMessage   `json:"metadata,omitempty"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Error       *string           `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Tasks       []PipelineTask    `json:"tasks,omitempty"`
}

// PipelineTask is a single step within a PipelineRun.
type PipelineTask struct {
	ID            uuid.UUID          `json:"id"`
	PipelineRunID uuid.UUID          `json:"pipeline_run_id"`
	RiverJobID    *int64             `json:"river_job_id,omitempty"`
	Name          string             `json:"name"`
	Status        PipelineTaskStatus `json:"status"`
	Attempt       int                `json:"attempt"`
	StartedAt     *time.Time         `json:"started_at,omitempty"`
	CompletedAt   *time.Time         `json:"completed_at,omitempty"`
	DurationMs    int                `json:"duration_ms,omitempty"`
	Error         *string            `json:"error,omitempty"`
	SortOrder     int                `json:"sort_order"`
}
