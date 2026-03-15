package jobs

import (
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/store"
)

// JobContext holds runtime worker dependencies that are initialized after
// worker registration (e.g. the River client used for job chaining).
type JobContext struct {
	mu     sync.RWMutex
	client *river.Client[pgx.Tx]
}

func NewJobContext() *JobContext {
	return &JobContext{}
}

func (c *JobContext) SetClient(client *river.Client[pgx.Tx]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}

func (c *JobContext) Client() *river.Client[pgx.Tx] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

// RegisterAllWorkers registers all worker types with the River workers registry.
func RegisterAllWorkers(workers *river.Workers, s *store.Store, cfg *config.Config) *JobContext {
	jobCtx := NewJobContext()

	river.AddWorker(workers, NewIngestWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewEmbedWorker(s, cfg))
	river.AddWorker(workers, NewFetchSignalsWorker(s, jobCtx))
	river.AddWorker(workers, NewClusterThemesWorker(s, jobCtx))
	river.AddWorker(workers, NewNameThemesWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewScoreCandidatesWorker(s, jobCtx))
	river.AddWorker(workers, NewWriteCandidatesWorker(s, jobCtx))
	river.AddWorker(workers, NewUpdateContextWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewSpecGenWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewFetchSpecWorker(s, jobCtx))
	river.AddWorker(workers, NewIndexRepoWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewBuildContextWorker(s, jobCtx))
	river.AddWorker(workers, NewGenerateCodeWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewPrepareContextWorker(s, jobCtx))
	river.AddWorker(workers, NewProvisionSandboxWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewRunAgentWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewValidateOutputWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewCreatePRWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewNotifyWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewDigestAllProjectsWorker(s, jobCtx))
	river.AddWorker(workers, NewCopilotReviewWorker(s, cfg))
	river.AddWorker(workers, NewNangoSyncWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewSyncAllIntegrationsWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewIntercomSyncWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewSlackSyncWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewLinearSyncWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewJiraSyncWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewSendEmailWorker(s, cfg, jobCtx))
	river.AddWorker(workers, NewDigestEmailsWorker(s, cfg))

	return jobCtx
}
