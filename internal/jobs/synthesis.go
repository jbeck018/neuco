package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/neuco-ai/neuco/internal/config"
	"github.com/neuco-ai/neuco/internal/domain"
	"github.com/neuco-ai/neuco/internal/store"
)

// FetchSignalsWorker fetches recent signals and enqueues embedding.
type FetchSignalsWorker struct {
	river.WorkerDefaults[FetchSignalsJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewFetchSignalsWorker(s *store.Store, jobCtx *JobContext) *FetchSignalsWorker {
	return &FetchSignalsWorker{store: s, jobCtx: jobCtx}
}
func (w *FetchSignalsWorker) Work(ctx context.Context, job *river.Job[FetchSignalsJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("fetching signals for synthesis", "project_id", job.Args.ProjectID)

	// Get unembedded signals and enqueue embedding first
	unembedded, err := w.store.ListUnembeddedSignals(ctx, job.Args.ProjectID, 500)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		return err
	}

	if len(unembedded) > 0 {
		slog.Info("found unembedded signals", "count", len(unembedded))
		// Batch embed in chunks of 100
		for i := 0; i < len(unembedded); i += 100 {
			end := i + 100
			if end > len(unembedded) {
				end = len(unembedded)
			}
			ids := make([]uuid.UUID, end-i)
			for j, s := range unembedded[i:end] {
				ids[j] = s.ID
			}

			// Find the embed_missing task ID
			run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
			var embedTaskID uuid.UUID
			if run != nil {
				for _, t := range run.Tasks {
					if t.Name == "embed_missing" {
						embedTaskID = t.ID
						break
					}
				}
			}

			client := w.jobCtx.Client()
			if client != nil {
				_, err := client.Insert(ctx, EmbedJobArgs{
					ProjectID: job.Args.ProjectID,
					SignalIDs: ids,
					RunID:     job.Args.RunID,
					TaskID:    embedTaskID,
				}, &river.InsertOpts{Queue: "synthesis"})
				if err != nil {
					slog.Error("failed to enqueue embed job", "error", err)
				}
			}
		}
	}
	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: enqueue cluster_themes
	client := w.jobCtx.Client()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var clusterTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "cluster_themes" {
					clusterTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, ClusterThemesJobArgs{
			ProjectID: job.Args.ProjectID,
			RunID:     job.Args.RunID,
			TaskID:    clusterTaskID,
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to chain cluster_themes job", "error", err)
		}
	}

	return nil
}

// ClusterThemesWorker groups signals by embedding similarity.
type ClusterThemesWorker struct {
	river.WorkerDefaults[ClusterThemesJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewClusterThemesWorker(s *store.Store, jobCtx *JobContext) *ClusterThemesWorker {
	return &ClusterThemesWorker{store: s, jobCtx: jobCtx}
}
func (w *ClusterThemesWorker) Work(ctx context.Context, job *river.Job[ClusterThemesJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("clustering themes", "project_id", job.Args.ProjectID)

	// Fetch all embedded signals for this project
	signals, err := w.store.ListEmbeddedSignals(ctx, job.Args.ProjectID, 1000)
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		return err
	}

	if len(signals) < 3 {
		slog.Info("not enough signals for clustering", "count", len(signals))
		CompleteTask(ctx, w.store, job.Args.TaskID, start)
		CheckPipelineCompletion(ctx, w.store, job.Args.RunID)
		return nil
	}

	// Determine number of clusters: sqrt(n/2), min 2, max 20
	k := int(math.Ceil(math.Sqrt(float64(len(signals)) / 2.0)))
	if k < 2 {
		k = 2
	}
	if k > 20 {
		k = 20
	}

	// Simple k-means clustering on embeddings
	clusters := kMeansClustering(signals, k)

	// Store clusters as feature candidates
	var clusterIDs []uuid.UUID
	for _, cluster := range clusters {
		if len(cluster.Signals) == 0 {
			continue
		}

		candidate := domain.FeatureCandidate{
			ID:          uuid.New(),
			ProjectID:   job.Args.ProjectID,
			Title:       fmt.Sprintf("Theme: %d signals", len(cluster.Signals)),
			SignalCount: len(cluster.Signals),
			Status:      domain.CandidateStatusNew,
		}

		inserted, err := w.store.UpsertCandidate(ctx, candidate)
		if err != nil {
			slog.Error("failed to upsert candidate", "error", err)
			continue
		}

		candidate = inserted
		// Link signals to candidate
		for _, sig := range cluster.Signals {
			if err := w.store.LinkCandidateSignal(ctx, candidate.ID, sig.ID, sig.Relevance); err != nil {
				slog.Error("failed to link signal to candidate", "error", err)
			}
		}

		clusterIDs = append(clusterIDs, candidate.ID)
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: enqueue name_themes
	client := w.jobCtx.Client()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var nameTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "name_themes" {
					nameTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, NameThemesJobArgs{
			ProjectID:  job.Args.ProjectID,
			ClusterIDs: clusterIDs,
			RunID:      job.Args.RunID,
			TaskID:     nameTaskID,
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to chain name_themes job", "error", err)
		}
	}

	return nil
}

type clusterResult struct {
	Centroid []float32
	Signals  []signalWithRelevance
}

type signalWithRelevance struct {
	ID        uuid.UUID
	Relevance float64
}

// kMeansClustering performs k-means on signal embeddings.
func kMeansClustering(signals []domain.Signal, k int) []clusterResult {
	if len(signals) == 0 || k <= 0 {
		return nil
	}

	dim := len(signals[0].Embedding)
	if dim == 0 {
		return nil
	}

	// Initialize centroids using first k signals
	centroids := make([][]float32, k)
	for i := 0; i < k && i < len(signals); i++ {
		centroids[i] = make([]float32, dim)
		copy(centroids[i], signals[i].Embedding)
	}

	assignments := make([]int, len(signals))
	maxIter := 50

	for iter := 0; iter < maxIter; iter++ {
		changed := false

		// Assign each signal to nearest centroid
		for i, sig := range signals {
			if len(sig.Embedding) != dim {
				continue
			}
			minDist := math.MaxFloat64
			bestCluster := 0
			for c, centroid := range centroids {
				dist := cosineDistance(sig.Embedding, centroid)
				if dist < minDist {
					minDist = dist
					bestCluster = c
				}
			}
			if assignments[i] != bestCluster {
				assignments[i] = bestCluster
				changed = true
			}
		}

		if !changed {
			break
		}

		// Recompute centroids
		counts := make([]int, k)
		newCentroids := make([][]float32, k)
		for i := range newCentroids {
			newCentroids[i] = make([]float32, dim)
		}

		for i, sig := range signals {
			c := assignments[i]
			if len(sig.Embedding) != dim {
				continue
			}
			counts[c]++
			for d := 0; d < dim; d++ {
				newCentroids[c][d] += sig.Embedding[d]
			}
		}

		for c := range centroids {
			if counts[c] > 0 {
				for d := 0; d < dim; d++ {
					newCentroids[c][d] /= float32(counts[c])
				}
				centroids[c] = newCentroids[c]
			}
		}
	}

	// Build results
	results := make([]clusterResult, k)
	for c := range results {
		results[c].Centroid = centroids[c]
	}

	for i, sig := range signals {
		c := assignments[i]
		dist := cosineDistance(sig.Embedding, centroids[c])
		relevance := 1.0 - dist // higher = more relevant
		results[c].Signals = append(results[c].Signals, signalWithRelevance{
			ID:        sig.ID,
			Relevance: relevance,
		})
	}

	return results
}

// cosineDistance computes 1 - cosine_similarity between two vectors.
func cosineDistance(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1.0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 1.0
	}
	similarity := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	return 1.0 - similarity
}

// NameThemesWorker names each cluster using an LLM call.
type NameThemesWorker struct {
	river.WorkerDefaults[NameThemesJobArgs]
	store  *store.Store
	cfg    *config.Config
	jobCtx *JobContext
}

func NewNameThemesWorker(s *store.Store, cfg *config.Config, jobCtx *JobContext) *NameThemesWorker {
	return &NameThemesWorker{store: s, cfg: cfg, jobCtx: jobCtx}
}
func (w *NameThemesWorker) Work(ctx context.Context, job *river.Job[NameThemesJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("naming themes", "project_id", job.Args.ProjectID, "clusters", len(job.Args.ClusterIDs))

	// Fetch prior project context to enrich theme naming.
	var priorContextHint string
	priorContexts, err := w.store.ListProjectContextsInternal(ctx, job.Args.ProjectID, 10)
	if err == nil && len(priorContexts) > 0 {
		priorContextHint = "\n\nPrior project context (use this to provide more informed naming):\n"
		for _, pc := range priorContexts {
			priorContextHint += fmt.Sprintf("- [%s] %s\n", pc.Category, pc.Title)
		}
	}

	for _, candidateID := range job.Args.ClusterIDs {
		candidate, err := w.store.GetCandidateInternal(ctx, candidateID)
		if err != nil {
			slog.Error("failed to get candidate", "id", candidateID, "error", err)
			continue
		}

		// Get representative signals for this candidate
		signals, err := w.store.GetCandidateSignals(ctx, candidateID, 10)
		if err != nil {
			slog.Error("failed to get candidate signals", "id", candidateID, "error", err)
			continue
		}

		// Build context from signal content
		var signalTexts string
		for i, sig := range signals {
			if i > 0 {
				signalTexts += "\n---\n"
			}
			signalTexts += sig.Content
		}

		// Call LLM to name the theme
		llmStart := time.Now()
		title, summary, llmResp, err := nameThemeViaLLM(ctx, w.cfg.AnthropicAPIKey, signalTexts+priorContextHint)
		llmLatency := trackDuration(llmStart)
		if llmResp != nil {
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			recordLLMCall(ctx, w.store, job.Args.ProjectID,
				ptrUUID(job.Args.RunID), ptrUUID(job.Args.TaskID),
				domain.LLMProviderAnthropic, "claude-haiku-4-5-20251001",
				domain.LLMCallTypeThemeNaming,
				llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, llmLatency,
				errMsg)
		}
		if err != nil {
			slog.Error("failed to name theme", "id", candidateID, "error", err)
			continue
		}

		candidate.Title = title
		candidate.ProblemSummary = summary
		if err := w.store.UpdateCandidateTheme(ctx, candidateID, title, summary); err != nil {
			slog.Error("failed to update candidate theme", "id", candidateID, "error", err)
		}
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: score_candidates
	client := w.jobCtx.Client()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var scoreTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "score_candidates" {
					scoreTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, ScoreCandidatesJobArgs{
			ProjectID: job.Args.ProjectID,
			RunID:     job.Args.RunID,
			TaskID:    scoreTaskID,
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to chain score_candidates job", "error", err)
		}
	}

	return nil
}

func nameThemeViaLLM(ctx context.Context, apiKey string, signalTexts string) (string, string, *anthropicResponse, error) {
	if apiKey == "" {
		return "Unnamed Theme", "No API key configured for theme naming.", nil, nil
	}

	prompt := fmt.Sprintf(`You are analyzing customer signals for a product team. Given the following customer feedback signals, provide:
1. A concise theme title (max 10 words)
2. A problem summary (2-3 sentences describing the underlying user pain)

Respond in JSON format: {"title": "...", "summary": "..."}

Signals:
%s`, signalTexts)

	payload := map[string]interface{}{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 300,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", nil, err
	}

	req, err := newHTTPRequest(ctx, "POST", "https://api.anthropic.com/v1/messages", body)
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := doHTTPRequest(req)
	if err != nil {
		return "", "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", nil, err
	}

	if len(result.Content) == 0 {
		return "Unnamed Theme", "", &result, nil
	}

	var parsed struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		return result.Content[0].Text, "", &result, nil
	}

	return parsed.Title, parsed.Summary, &result, nil
}

// ScoreCandidatesWorker scores feature candidates by frequency, recency, and weight.
type ScoreCandidatesWorker struct {
	river.WorkerDefaults[ScoreCandidatesJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewScoreCandidatesWorker(s *store.Store, jobCtx *JobContext) *ScoreCandidatesWorker {
	return &ScoreCandidatesWorker{store: s, jobCtx: jobCtx}
}
func (w *ScoreCandidatesWorker) Work(ctx context.Context, job *river.Job[ScoreCandidatesJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("scoring candidates", "project_id", job.Args.ProjectID)

	candidates, _, err := w.store.ListProjectCandidates(ctx, job.Args.ProjectID, store.PageParams{Limit: 100, Offset: 0})
	if err != nil {
		FailTask(ctx, w.store, job.Args.TaskID, err)
		return err
	}

	now := time.Now()
	for _, candidate := range candidates {
		// Get signals for this candidate
		signals, err := w.store.GetCandidateSignals(ctx, candidate.ID, 100)
		if err != nil {
			slog.Error("failed to get candidate signals for scoring", "id", candidate.ID, "error", err)
			continue
		}

		// Score = frequency * recency * diversity
		frequency := float64(len(signals))

		// Recency: average days since signal, inverted
		var totalRecency float64
		for _, sig := range signals {
			daysSince := now.Sub(sig.IngestedAt).Hours() / 24
			if daysSince < 1 {
				daysSince = 1
			}
			totalRecency += 1.0 / daysSince
		}
		recency := totalRecency / math.Max(frequency, 1)

		// Source diversity: number of unique sources
		sources := make(map[domain.SignalSource]bool)
		for _, sig := range signals {
			sources[sig.Source] = true
		}
		diversity := float64(len(sources))

		score := frequency * recency * diversity
		if err := w.store.UpdateCandidateScore(ctx, candidate.ID, score); err != nil {
			slog.Error("failed to update candidate score", "id", candidate.ID, "error", err)
		}
	}

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: write_candidates
	client := w.jobCtx.Client()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var writeTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "write_candidates" {
					writeTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, WriteCandidatesJobArgs{
			ProjectID: job.Args.ProjectID,
			RunID:     job.Args.RunID,
			TaskID:    writeTaskID,
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to chain write_candidates job", "error", err)
		}
	}

	return nil
}

// WriteCandidatesWorker finalizes candidates and marks the pipeline complete.
type WriteCandidatesWorker struct {
	river.WorkerDefaults[WriteCandidatesJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewWriteCandidatesWorker(s *store.Store, jobCtx *JobContext) *WriteCandidatesWorker {
	return &WriteCandidatesWorker{store: s, jobCtx: jobCtx}
}
func (w *WriteCandidatesWorker) Work(ctx context.Context, job *river.Job[WriteCandidatesJobArgs]) error {
	start := time.Now()
	StartTask(ctx, w.store, job.Args.TaskID)

	slog.Info("finalizing candidates", "project_id", job.Args.ProjectID)

	// Nothing extra to do — candidates were already written by ClusterThemesWorker
	// and scored by ScoreCandidatesWorker. This step exists for pipeline completeness
	// and to trigger the copilot review.

	CompleteTask(ctx, w.store, job.Args.TaskID, start)

	// Chain: update_context (accumulates project memory from synthesis results)
	client := w.jobCtx.Client()
	if client != nil {
		run, _ := w.store.GetPipelineRun(ctx, job.Args.RunID)
		var contextTaskID uuid.UUID
		if run != nil {
			for _, t := range run.Tasks {
				if t.Name == "update_context" {
					contextTaskID = t.ID
					break
				}
			}
		}

		_, err := client.Insert(ctx, UpdateContextJobArgs{
			ProjectID: job.Args.ProjectID,
			RunID:     job.Args.RunID,
			TaskID:    contextTaskID,
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to chain update_context job", "error", err)
		}
	}

	return nil
}

// DigestAllProjectsWorker runs synthesis for all active projects (weekly cron).
type DigestAllProjectsWorker struct {
	river.WorkerDefaults[DigestAllProjectsJobArgs]
	store  *store.Store
	jobCtx *JobContext
}

func NewDigestAllProjectsWorker(s *store.Store, jobCtx *JobContext) *DigestAllProjectsWorker {
	return &DigestAllProjectsWorker{store: s, jobCtx: jobCtx}
}
func (w *DigestAllProjectsWorker) Work(ctx context.Context, _ *river.Job[DigestAllProjectsJobArgs]) error {
	slog.Info("running weekly digest for all projects")

	projects, err := w.store.ListAllActiveProjects(ctx)
	if err != nil {
		return err
	}

	client := w.jobCtx.Client()
	if client == nil {
		return fmt.Errorf("river client not available")
	}
	for _, project := range projects {
		runID, taskIDs, err := CreateSynthesisPipeline(ctx, w.store, project.ID)
		if err != nil {
			slog.Error("failed to create synthesis pipeline", "project_id", project.ID, "error", err)
			continue
		}

		_, err = client.Insert(ctx, FetchSignalsJobArgs{
			ProjectID: project.ID,
			RunID:     runID,
			TaskID:    taskIDs[0],
		}, &river.InsertOpts{Queue: "synthesis"})
		if err != nil {
			slog.Error("failed to enqueue synthesis", "project_id", project.ID, "error", err)
		}
	}

	return nil
}
