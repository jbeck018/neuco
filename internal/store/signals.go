package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// SignalFilters holds optional filter criteria for ListProjectSignals.
type SignalFilters struct {
	Sources           []string   // domain.SignalSource values; nil means no filter
	Types             []string   // domain.SignalType values; nil means no filter
	From              *time.Time // inclusive lower bound on occurred_at
	To                *time.Time // inclusive upper bound on occurred_at
	ExcludeDuplicates bool       // if true, omit signals where duplicate_of_id IS NOT NULL
}

// SignalPage is the result type for paginated signal queries.
type SignalPage struct {
	Signals []domain.Signal
	Total   int
}

const signalColumns = `
	id, project_id, source, source_ref, type, content, metadata,
	occurred_at, ingested_at, content_hash, duplicate_of_id`

// ErrDuplicateSignal is returned when an exact content hash match is found
// in the same project during insertion.
var ErrDuplicateSignal = fmt.Errorf("duplicate signal")

// InsertSignal writes a single signal to the database and returns it with the
// server-assigned ID and ingested_at timestamp. If an exact content-hash
// duplicate exists in the same project, ErrDuplicateSignal is returned along
// with the existing signal.
func (s *Store) InsertSignal(ctx context.Context, sig domain.Signal) (domain.Signal, error) {
	meta := sig.Metadata
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}

	hash := ContentHash(sig.Content)

	// Check for exact duplicate by content hash within the same project.
	existing, err := s.findByContentHash(ctx, sig.ProjectID, hash)
	if err == nil {
		return existing, ErrDuplicateSignal
	}

	const q = `
		INSERT INTO signals
		       (project_id, source, source_ref, type, content, metadata, occurred_at, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + signalColumns

	row := s.pool.QueryRow(ctx, q,
		sig.ProjectID,
		sig.Source,
		sig.SourceRef,
		sig.Type,
		sig.Content,
		meta,
		sig.OccurredAt,
		hash,
	)
	return scanSignal(row)
}

// InsertSignalTx writes a signal within an already-open transaction. This is
// used by the webhook handler to atomically insert the signal and enqueue the
// River ingest job.
func (s *Store) InsertSignalTx(ctx context.Context, tx pgx.Tx, sig domain.Signal) (domain.Signal, error) {
	meta := sig.Metadata
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}

	hash := ContentHash(sig.Content)

	// Check for exact duplicate within the transaction.
	const checkQ = `
		SELECT ` + signalColumns + `
		FROM   signals
		WHERE  project_id = $1 AND content_hash = $2 AND duplicate_of_id IS NULL
		LIMIT  1`
	row := tx.QueryRow(ctx, checkQ, sig.ProjectID, hash)
	existing, err := scanSignal(row)
	if err == nil {
		return existing, ErrDuplicateSignal
	}

	const q = `
		INSERT INTO signals
		       (project_id, source, source_ref, type, content, metadata, occurred_at, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + signalColumns

	row = tx.QueryRow(ctx, q,
		sig.ProjectID,
		sig.Source,
		sig.SourceRef,
		sig.Type,
		sig.Content,
		meta,
		sig.OccurredAt,
		hash,
	)
	return scanSignal(row)
}

// GetSignal returns the signal with the given ID scoped to projectID.
func (s *Store) GetSignal(ctx context.Context, projectID, signalID uuid.UUID) (domain.Signal, error) {
	const q = `
		SELECT ` + signalColumns + `
		FROM   signals
		WHERE  id = $1 AND project_id = $2`

	row := s.pool.QueryRow(ctx, q, signalID, projectID)
	sig, err := scanSignal(row)
	if err != nil {
		return domain.Signal{}, fmt.Errorf("store.GetSignal: %w", err)
	}
	return sig, nil
}

// ListProjectSignals returns a paginated, optionally filtered list of signals
// for a project. The WHERE clause is built dynamically using only
// parameterised placeholders — never string concatenation of user data.
func (s *Store) ListProjectSignals(
	ctx context.Context,
	projectID uuid.UUID,
	filters SignalFilters,
	pp PageParams,
) (SignalPage, error) {
	// args[0] is always the projectID.
	args := []any{projectID}
	conds := []string{"project_id = $1"}

	if len(filters.Sources) > 0 {
		args = append(args, filters.Sources)
		conds = append(conds, fmt.Sprintf("source = ANY($%d)", len(args)))
	}
	if len(filters.Types) > 0 {
		args = append(args, filters.Types)
		conds = append(conds, fmt.Sprintf("type = ANY($%d)", len(args)))
	}
	if filters.From != nil {
		args = append(args, *filters.From)
		conds = append(conds, fmt.Sprintf("occurred_at >= $%d", len(args)))
	}
	if filters.To != nil {
		args = append(args, *filters.To)
		conds = append(conds, fmt.Sprintf("occurred_at <= $%d", len(args)))
	}
	if filters.ExcludeDuplicates {
		conds = append(conds, "duplicate_of_id IS NULL")
	}

	where := "WHERE " + strings.Join(conds, " AND ")

	// Count query shares the same WHERE clause.
	countQuery := "SELECT COUNT(*) FROM signals " + where
	var total int
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return SignalPage{}, fmt.Errorf("store.ListProjectSignals count: %w", err)
	}

	// Data query appends the pagination parameters after the existing args.
	args = append(args, pp.Limit, pp.Offset)
	dataQuery := fmt.Sprintf(
		"SELECT %s FROM signals %s ORDER BY occurred_at DESC LIMIT $%d OFFSET $%d",
		signalColumns, where, len(args)-1, len(args),
	)

	rows, err := s.pool.Query(ctx, dataQuery, args...)
	if err != nil {
		return SignalPage{}, fmt.Errorf("store.ListProjectSignals: %w", err)
	}
	defer rows.Close()

	sigs, err := collectSignals(rows)
	if err != nil {
		return SignalPage{}, fmt.Errorf("store.ListProjectSignals: %w", err)
	}
	return SignalPage{Signals: sigs, Total: total}, nil
}

// DeleteSignal removes a signal scoped to projectID.
func (s *Store) DeleteSignal(ctx context.Context, projectID, signalID uuid.UUID) error {
	const q = `DELETE FROM signals WHERE id = $1 AND project_id = $2`
	ct, err := s.pool.Exec(ctx, q, signalID, projectID)
	if err != nil {
		return fmt.Errorf("store.DeleteSignal: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.DeleteSignal: signal %s not found in project %s", signalID, projectID)
	}
	return nil
}

// UpdateSignalEmbedding stores the vector embedding for a signal. The embedding
// is cast to a pgvector vector type by the database.
func (s *Store) UpdateSignalEmbedding(ctx context.Context, signalID uuid.UUID, embedding []float32) error {
	// pgvector expects the embedding as a Go []float32 when using the
	// pgvector/pgvector-go codec, or as a formatted string literal.
	// We format it as a PostgreSQL array literal which pgvector accepts.
	lit := float32SliceToVectorLiteral(embedding)
	const q = `UPDATE signals SET embedding = $2::vector WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, signalID, lit)
	if err != nil {
		return fmt.Errorf("store.UpdateSignalEmbedding: %w", err)
	}
	return nil
}

// ListUnembeddedSignals returns up to limit signals that have no embedding yet,
// ordered by ingested_at so the oldest signals are embedded first.
func (s *Store) ListUnembeddedSignals(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.Signal, error) {
	const q = `
		SELECT ` + signalColumns + `
		FROM   signals
		WHERE  project_id = $1
		  AND  embedding IS NULL
		ORDER  BY ingested_at
		LIMIT  $2`

	rows, err := s.pool.Query(ctx, q, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("store.ListUnembeddedSignals: %w", err)
	}
	defer rows.Close()

	sigs, err := collectSignals(rows)
	if err != nil {
		return nil, fmt.Errorf("store.ListUnembeddedSignals: %w", err)
	}
	return sigs, nil
}

// scanSignal reads a single Signal from any pgx row-like value.
// The embedding column is intentionally omitted from the default select list
// for performance; it is fetched only by specialised queries.
func scanSignal(row pgx.Row) (domain.Signal, error) {
	var sig domain.Signal
	var meta []byte
	var contentHash *string
	var duplicateOfID *uuid.UUID
	err := row.Scan(
		&sig.ID,
		&sig.ProjectID,
		&sig.Source,
		&sig.SourceRef,
		&sig.Type,
		&sig.Content,
		&meta,
		&sig.OccurredAt,
		&sig.IngestedAt,
		&contentHash,
		&duplicateOfID,
	)
	if err != nil {
		return domain.Signal{}, err
	}
	if meta != nil {
		sig.Metadata = json.RawMessage(meta)
	} else {
		sig.Metadata = json.RawMessage(`{}`)
	}
	if contentHash != nil {
		sig.ContentHash = *contentHash
	}
	sig.DuplicateOfID = duplicateOfID
	return sig, nil
}

// collectSignals iterates over pgx rows and collects Signal values.
func collectSignals(rows pgx.Rows) ([]domain.Signal, error) {
	var sigs []domain.Signal
	for rows.Next() {
		sig, err := scanSignal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		sigs = append(sigs, sig)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return sigs, nil
}

// ContentHash returns the SHA-256 hex digest of normalised (lower-cased,
// trimmed) content, used for exact deduplication.
func ContentHash(content string) string {
	normalised := strings.ToLower(strings.TrimSpace(content))
	h := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(h[:])
}

// findByContentHash looks up a non-duplicate signal with the given content hash
// in a project. Returns the signal if found, otherwise an error.
func (s *Store) findByContentHash(ctx context.Context, projectID uuid.UUID, hash string) (domain.Signal, error) {
	const q = `
		SELECT ` + signalColumns + `
		FROM   signals
		WHERE  project_id = $1 AND content_hash = $2 AND duplicate_of_id IS NULL
		LIMIT  1`

	row := s.pool.QueryRow(ctx, q, projectID, hash)
	return scanSignal(row)
}

// FindNearDuplicateSignal searches for an existing signal in the same project
// whose embedding has cosine similarity >= threshold to the given embedding.
// Returns the matching signal ID or nil if no near-duplicate exists.
func (s *Store) FindNearDuplicateSignal(ctx context.Context, projectID uuid.UUID, signalID uuid.UUID, embedding []float32, threshold float64) (*uuid.UUID, error) {
	lit := float32SliceToVectorLiteral(embedding)
	const q = `
		SELECT id
		FROM   signals
		WHERE  project_id = $1
		  AND  id != $2
		  AND  duplicate_of_id IS NULL
		  AND  embedding IS NOT NULL
		  AND  1 - (embedding <=> $3::vector) >= $4
		ORDER  BY embedding <=> $3::vector
		LIMIT  1`

	var matchID uuid.UUID
	err := s.pool.QueryRow(ctx, q, projectID, signalID, lit, threshold).Scan(&matchID)
	if err != nil {
		return nil, err
	}
	return &matchID, nil
}

// MarkAsDuplicate sets the duplicate_of_id field on a signal.
func (s *Store) MarkAsDuplicate(ctx context.Context, signalID, originalID uuid.UUID) error {
	const q = `UPDATE signals SET duplicate_of_id = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, signalID, originalID)
	if err != nil {
		return fmt.Errorf("store.MarkAsDuplicate: %w", err)
	}
	return nil
}

// DuplicateCount returns the number of signals that are duplicates of a given
// original signal.
func (s *Store) DuplicateCount(ctx context.Context, signalID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM signals WHERE duplicate_of_id = $1`
	var count int
	err := s.pool.QueryRow(ctx, q, signalID).Scan(&count)
	return count, err
}

// float32SliceToVectorLiteral converts a []float32 to a pgvector literal
// string of the form "[0.1,0.2,…]".
func float32SliceToVectorLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}
