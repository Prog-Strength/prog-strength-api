package weather

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrBudgetExhausted means today's reservation would cross the active
// ceiling; the provider call must not happen.
var ErrBudgetExhausted = errors.New("weather: daily call budget exhausted")

// BudgetLedger is the durable spend ledger. Reservation happens BEFORE the
// HTTP request: a crash between reserving and calling over-counts by one,
// which is the safe direction for a spend cap.
type BudgetLedger struct {
	db  *sql.DB
	now func() time.Time
}

func NewBudgetLedger(db *sql.DB) *BudgetLedger {
	return &BudgetLedger{db: db, now: time.Now}
}

// Reserve atomically claims n calls against today's UTC row, creating it if
// absent. n is a parameter because a full refresh of one location is up to
// three calls and reserving them atomically avoids a half-refreshed location
// that consumed budget for a card it cannot draw.
func (l *BudgetLedger) Reserve(ctx context.Context, n, ceiling int) error {
	day := l.now().UTC().Format("2006-01-02")
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO weather_call_budget (usage_date, calls_used, updated_at)
		VALUES (?, 0, ?)
		ON CONFLICT(usage_date) DO NOTHING
	`, day, l.now().UTC()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE weather_call_budget
		SET calls_used = calls_used + ?, updated_at = ?
		WHERE usage_date = ? AND calls_used + ? <= ?
	`, n, l.now().UTC(), day, n, ceiling)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrBudgetExhausted
	}
	return tx.Commit()
}

// UsedToday reads today's UTC row; 0 when the day has no reservations yet.
func (l *BudgetLedger) UsedToday(ctx context.Context) (int, error) {
	day := l.now().UTC().Format("2006-01-02")
	var used int
	err := l.db.QueryRowContext(ctx,
		`SELECT calls_used FROM weather_call_budget WHERE usage_date = ?`, day,
	).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return used, err
}
