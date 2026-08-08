package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrQuoteRerollNotFound is returned by QuoteRerollRepository.Get when the user
// has never rerolled. Callers resolve it to offset 0 — the day's quote.
//
// A sentinel rather than a zero value because offset 0 is itself a legitimate
// stored state: a user who rerolls all the way around the corpus lands back on
// it, and "wrapped to 0" must not be confused with "never touched the button".
var ErrQuoteRerollNotFound = errors.New("dashboard: quote reroll not found")

// QuoteReroll is a user's stored reroll position: the offset they last advanced
// to, and the local calendar date they did it on.
//
// LocalDate is the expiry mechanism. The read path compares it against the
// user's local date now and ignores a stale one, so a reroll lapses at the
// user's own local midnight — the same boundary the daily quote turns over on.
type QuoteReroll struct {
	LocalDate string
	Offset    int
}

// QuoteRerollRepository persists one reroll position per user.
type QuoteRerollRepository interface {
	// Get returns the user's stored reroll, or ErrQuoteRerollNotFound when
	// they have no row. It does NOT interpret LocalDate — deciding whether the
	// stored date is still today is the caller's job, because only the caller
	// knows the user's timezone.
	Get(ctx context.Context, userID string) (QuoteReroll, error)
	// Upsert writes the user's reroll position, replacing any existing row.
	Upsert(ctx context.Context, userID, localDate string, offset int) error
}

var _ QuoteRerollRepository = (*SQLiteQuoteRerollRepository)(nil)

type SQLiteQuoteRerollRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteQuoteRerollRepository(db *sql.DB) *SQLiteQuoteRerollRepository {
	return &SQLiteQuoteRerollRepository{db: db, now: time.Now}
}

func (r *SQLiteQuoteRerollRepository) Get(ctx context.Context, userID string) (QuoteReroll, error) {
	var out QuoteReroll
	err := r.db.QueryRowContext(ctx,
		`SELECT local_date, quote_offset FROM user_dashboard_quote_rerolls WHERE user_id = ?`,
		userID).Scan(&out.LocalDate, &out.Offset)
	if errors.Is(err, sql.ErrNoRows) {
		return QuoteReroll{}, ErrQuoteRerollNotFound
	}
	if err != nil {
		return QuoteReroll{}, fmt.Errorf("dashboard: get quote reroll: %w", err)
	}
	return out, nil
}

func (r *SQLiteQuoteRerollRepository) Upsert(ctx context.Context, userID, localDate string, offset int) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_dashboard_quote_rerolls (user_id, local_date, quote_offset, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			local_date   = excluded.local_date,
			quote_offset = excluded.quote_offset,
			updated_at   = excluded.updated_at
	`, userID, localDate, offset, r.now().UTC())
	if err != nil {
		return fmt.Errorf("dashboard: upsert quote reroll: %w", err)
	}
	return nil
}
