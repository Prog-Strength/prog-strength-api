package calendarconn

import (
	"context"
	"sync"
	"time"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// record is the full stored row, including token material, kept private to
// the memory repo so Get never leaks the token through the Connection view.
type record struct {
	conn  Connection
	enc   []byte
	nonce []byte
}

// MemoryRepository is the dev/test in-memory implementation, holding one
// record per user under a RW mutex — same pattern as the bodyweight package.
type MemoryRepository struct {
	mu   sync.RWMutex
	rows map[string]*record // user_id → record
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{rows: make(map[string]*record)}
}

func (r *MemoryRepository) Upsert(ctx context.Context, userID string, refreshTokenEnc, nonce []byte, calendarID, scopes string, now time.Time) error {
	now = now.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	connectedAt := now
	if existing, ok := r.rows[userID]; ok {
		// Preserve the original connection timestamp across re-connects.
		connectedAt = existing.conn.ConnectedAt
	}

	// Copy the byte slices so callers can't mutate stored state.
	enc := append([]byte(nil), refreshTokenEnc...)
	non := append([]byte(nil), nonce...)

	r.rows[userID] = &record{
		conn: Connection{
			UserID:           userID,
			GoogleCalendarID: calendarID,
			Scopes:           scopes,
			Status:           StatusConnected,
			ConnectedAt:      connectedAt,
			UpdatedAt:        now,
		},
		enc:   enc,
		nonce: non,
	}
	return nil
}

func (r *MemoryRepository) Get(ctx context.Context, userID string) (*Connection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.rows[userID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := rec.conn
	return &cp, nil
}

func (r *MemoryRepository) GetRefreshToken(ctx context.Context, userID string) (enc, nonce []byte, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.rows[userID]
	if !ok {
		return nil, nil, ErrNotFound
	}
	return append([]byte(nil), rec.enc...), append([]byte(nil), rec.nonce...), nil
}

func (r *MemoryRepository) SetStatus(ctx context.Context, userID string, status Status, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.rows[userID]
	if !ok {
		return ErrNotFound
	}
	rec.conn.Status = status
	rec.conn.UpdatedAt = now.UTC()
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.rows[userID]; !ok {
		return ErrNotFound
	}
	delete(r.rows, userID)
	return nil
}

func (r *MemoryRepository) Exists(ctx context.Context, userID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.rows[userID]
	return ok, nil
}
