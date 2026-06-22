package vectormemory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) Insert(ctx context.Context, m NewMemory) (id int64, err error) {
	blob, err := sqlite_vec.SerializeFloat32(m.Embedding)
	if err != nil {
		return 0, fmt.Errorf("vectormemory: serialize embedding: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("vectormemory: begin tx: %w", err)
	}
	// Roll back unless we reach the commit; a committed tx makes Rollback a
	// no-op that returns sql.ErrTxDone, which we deliberately ignore.
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO agent_memories (
			user_id, distilled_text, source_type, source_session_id,
			source_message_id, source_workout_id, embedding_model, embedding_dim, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.UserID,
		m.DistilledText,
		m.SourceType,
		nullableString(m.SourceSessionID),
		nullableInt64(m.SourceMessageID),
		nullableString(m.SourceWorkoutID),
		m.EmbeddingModel,
		m.EmbeddingDim,
		m.CreatedAt.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("vectormemory: insert agent_memories: %w", err)
	}

	id, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("vectormemory: last insert id: %w", err)
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO vec_agent_memories (memory_id, user_id, embedding)
		VALUES (?, ?, ?)
	`, id, m.UserID, blob); err != nil {
		return 0, fmt.Errorf("vectormemory: insert vec_agent_memories: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("vectormemory: commit: %w", err)
	}
	return id, nil
}

func (r *SQLiteRepository) Search(ctx context.Context, userID, model string, query []float32, k int, maxDistance float64) ([]Match, error) {
	blob, err := sqlite_vec.SerializeFloat32(query)
	if err != nil {
		return nil, fmt.Errorf("vectormemory: serialize query: %w", err)
	}

	// The single combined query works with this vec0 build: the KNN
	// constraints (embedding MATCH + k) live on the vec0 table while the
	// JOIN to agent_memories applies the model/superseded/text filters and
	// drops orphaned vec rows automatically. The distance cap is expressed
	// as (? <= 0 OR v.distance <= ?) so maxDistance <= 0 disables it.
	//
	// Ordering caveat: vec0 applies the k limit DURING the KNN scan, before
	// the JOIN-side filters. So nearer superseded/other-model rows can crowd
	// out valid ones within the k window, under-returning (never leaking — a
	// dropped match means the coach recalls less, never wrong). At launch the
	// index is single-model with no superseded rows, so this can't bite; if a
	// future model migration leaves a mixed index thin on recall, over-fetch
	// with an internal k multiplier here.
	rows, err := r.db.QueryContext(ctx, `
		SELECT am.distilled_text, v.distance, am.source_session_id, am.created_at
		FROM vec_agent_memories v
		JOIN agent_memories am ON am.id = v.memory_id
		WHERE v.user_id = ?
		  AND v.embedding MATCH ?
		  AND k = ?
		  AND am.embedding_model = ?
		  AND am.superseded_at IS NULL
		  AND (? <= 0 OR v.distance <= ?)
		ORDER BY v.distance
	`, userID, blob, k, model, maxDistance, maxDistance)
	if err != nil {
		return nil, fmt.Errorf("vectormemory: search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var matches []Match
	for rows.Next() {
		var (
			m          Match
			sourceSess sql.NullString
		)
		// source_session_id is nullable now (a workout-note memory has NULL),
		// so scan through a NullString; a NULL maps to "" — unused by the agent.
		if err := rows.Scan(&m.Text, &m.Distance, &sourceSess, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("vectormemory: scan match: %w", err)
		}
		m.SourceSessionID = sourceSess.String
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vectormemory: search rows: %w", err)
	}
	return matches, nil
}

func (r *SQLiteRepository) NearestDistance(ctx context.Context, userID, model string, query []float32) (float64, bool, error) {
	blob, err := sqlite_vec.SerializeFloat32(query)
	if err != nil {
		return 0, false, fmt.Errorf("vectormemory: serialize query: %w", err)
	}

	var distance float64
	err = r.db.QueryRowContext(ctx, `
		SELECT v.distance
		FROM vec_agent_memories v
		JOIN agent_memories am ON am.id = v.memory_id
		WHERE v.user_id = ?
		  AND v.embedding MATCH ?
		  AND k = 1
		  AND am.embedding_model = ?
		  AND am.superseded_at IS NULL
		ORDER BY v.distance
	`, userID, blob, model).Scan(&distance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("vectormemory: nearest distance: %w", err)
	}
	return distance, true, nil
}

func (r *SQLiteRepository) Dump(ctx context.Context, userID string, limit, offset int) ([]Memory, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, distilled_text, source_type, source_session_id,
		       source_message_id, source_workout_id, embedding_model, embedding_dim,
		       superseded_at, created_at
		FROM agent_memories
		WHERE (? = '' OR user_id = ?)
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, userID, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("vectormemory: dump query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var memories []Memory
	for rows.Next() {
		var (
			m            Memory
			sourceSess   sql.NullString
			sourceMsgID  sql.NullInt64
			sourceWkt    sql.NullString
			supersededAt sql.NullTime
		)
		if err := rows.Scan(
			&m.ID,
			&m.UserID,
			&m.DistilledText,
			&m.SourceType,
			&sourceSess,
			&sourceMsgID,
			&sourceWkt,
			&m.EmbeddingModel,
			&m.EmbeddingDim,
			&supersededAt,
			&m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("vectormemory: scan memory: %w", err)
		}
		if sourceSess.Valid {
			m.SourceSessionID = &sourceSess.String
		}
		if sourceMsgID.Valid {
			m.SourceMessageID = &sourceMsgID.Int64
		}
		if sourceWkt.Valid {
			m.SourceWorkoutID = &sourceWkt.String
		}
		if supersededAt.Valid {
			m.SupersededAt = &supersededAt.Time
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vectormemory: dump rows: %w", err)
	}
	return memories, nil
}

// nullableInt64 maps a *int64 to a sql.NullInt64 so a nil source_message_id
// is written as SQL NULL rather than 0.
func nullableInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// nullableString maps a *string to sql.NullString so a nil typed-FK column is
// written as SQL NULL rather than the empty string (which would violate the
// agent_memories CHECK / FK).
func nullableString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}
