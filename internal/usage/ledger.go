package usage

import (
	"context"
	"database/sql"
	"time"
)

// Ledger computes a user's daily external-API spend from telemetry.db.
// It reads two tables (agent_turns for Claude, agent_speak_calls for TTS)
// and prices the grouped totals through the configured PriceTable. Cost
// is derived at read time — there is no separate ledger table to keep in
// sync.
type Ledger struct {
	db     *sql.DB
	prices PriceTable
}

// NewLedger wires the ledger to the telemetry *sql.DB handle (separate
// from app.db) and the parsed price table.
func NewLedger(telemetryDB *sql.DB, prices PriceTable) *Ledger {
	return &Ledger{db: telemetryDB, prices: prices}
}

// SpendTodayUSD returns the user's total priced spend for the half-open
// window [startUTC, endUTC). It runs two grouped SUM queries (one per
// table), both hitting the (user_id, started_at) index, and aggregates
// app-side so prices stay in Go config rather than SQL. The whole query
// path is timed into api_usage_query_duration_seconds.
func (l *Ledger) SpendTodayUSD(ctx context.Context, userID string, startUTC, endUTC time.Time) (float64, error) {
	start := time.Now()
	defer func() { queryDuration.Observe(time.Since(start).Seconds()) }()

	total, err := l.claudeSpend(ctx, userID, startUTC, endUTC)
	if err != nil {
		return 0, err
	}
	ttsTotal, err := l.ttsSpend(ctx, userID, startUTC, endUTC)
	if err != nil {
		return 0, err
	}
	return total + ttsTotal, nil
}

func (l *Ledger) claudeSpend(ctx context.Context, userID string, startUTC, endUTC time.Time) (float64, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT model,
		       SUM(input_tokens),
		       SUM(output_tokens),
		       SUM(cache_creation_tokens),
		       SUM(cache_read_tokens)
		FROM agent_turns
		WHERE user_id = ? AND started_at >= ? AND started_at < ?
		GROUP BY model
	`, userID, startUTC, endUTC)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var total float64
	for rows.Next() {
		var model string
		var in, out, cacheCreate, cacheRead int64
		if err := rows.Scan(&model, &in, &out, &cacheCreate, &cacheRead); err != nil {
			return 0, err
		}
		total += l.prices.ClaudeCostUSD(model, in, out, cacheCreate, cacheRead)
	}
	return total, rows.Err()
}

func (l *Ledger) ttsSpend(ctx context.Context, userID string, startUTC, endUTC time.Time) (float64, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT model, SUM(chars)
		FROM agent_speak_calls
		WHERE user_id = ? AND started_at >= ? AND started_at < ?
		GROUP BY model
	`, userID, startUTC, endUTC)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var total float64
	for rows.Next() {
		var model string
		var chars int64
		if err := rows.Scan(&model, &chars); err != nil {
			return 0, err
		}
		total += l.prices.TTSCostUSD(model, chars)
	}
	return total, rows.Err()
}
