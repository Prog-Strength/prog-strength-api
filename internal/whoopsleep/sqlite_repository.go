package whoopsleep

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is the production implementation backed by the
// user_whoop_sleep table.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// sleepColumns is the read projection, shared by every SELECT so the column
// order can never drift from scanEntry's.
const sleepColumns = `
	id, user_id, whoop_sleep_id, date, is_nap, started_at, ended_at,
	timezone_offset, score_state,
	in_bed_milli, awake_milli, no_data_milli, light_sleep_milli,
	slow_wave_sleep_milli, rem_sleep_milli, sleep_cycle_count, disturbance_count,
	need_baseline_milli, need_from_sleep_debt_milli, need_from_strain_milli,
	need_from_nap_milli,
	respiratory_rate, performance_pct, consistency_pct, efficiency_pct,
	created_at, updated_at`

// Upsert inserts the (user_id, whoop_sleep_id) row or replaces its contents in
// a single statement. ON CONFLICT keeps it race-free vs. get-then-write. Every
// mutable column is overwritten — a re-scored record replaces a PENDING one
// wholesale — while id and created_at are preserved.
func (r *SQLiteRepository) Upsert(ctx context.Context, e Entry, now time.Time) error {
	now = now.UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_whoop_sleep (
			id, user_id, whoop_sleep_id, date, is_nap, started_at, ended_at,
			timezone_offset, score_state,
			in_bed_milli, awake_milli, no_data_milli, light_sleep_milli,
			slow_wave_sleep_milli, rem_sleep_milli, sleep_cycle_count, disturbance_count,
			need_baseline_milli, need_from_sleep_debt_milli, need_from_strain_milli,
			need_from_nap_milli,
			respiratory_rate, performance_pct, consistency_pct, efficiency_pct,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, whoop_sleep_id) DO UPDATE SET
			date                       = excluded.date,
			is_nap                     = excluded.is_nap,
			started_at                 = excluded.started_at,
			ended_at                   = excluded.ended_at,
			timezone_offset            = excluded.timezone_offset,
			score_state                = excluded.score_state,
			in_bed_milli               = excluded.in_bed_milli,
			awake_milli                = excluded.awake_milli,
			no_data_milli              = excluded.no_data_milli,
			light_sleep_milli          = excluded.light_sleep_milli,
			slow_wave_sleep_milli      = excluded.slow_wave_sleep_milli,
			rem_sleep_milli            = excluded.rem_sleep_milli,
			sleep_cycle_count          = excluded.sleep_cycle_count,
			disturbance_count          = excluded.disturbance_count,
			need_baseline_milli        = excluded.need_baseline_milli,
			need_from_sleep_debt_milli = excluded.need_from_sleep_debt_milli,
			need_from_strain_milli     = excluded.need_from_strain_milli,
			need_from_nap_milli        = excluded.need_from_nap_milli,
			respiratory_rate           = excluded.respiratory_rate,
			performance_pct            = excluded.performance_pct,
			consistency_pct            = excluded.consistency_pct,
			efficiency_pct             = excluded.efficiency_pct,
			updated_at                 = excluded.updated_at
	`,
		id.New(), e.UserID, e.WhoopSleepID, e.Date, e.IsNap, e.StartedAt, e.EndedAt,
		e.TimezoneOffset, e.ScoreState,
		nullInt(e.InBedMilli), nullInt(e.AwakeMilli), nullInt(e.NoDataMilli),
		nullInt(e.LightSleepMilli), nullInt(e.SlowWaveSleepMilli), nullInt(e.REMSleepMilli),
		nullInt(e.SleepCycleCount), nullInt(e.DisturbanceCount),
		nullInt(e.NeedBaselineMilli), nullInt(e.NeedFromSleepDebtMilli),
		nullInt(e.NeedFromStrainMilli), nullInt(e.NeedFromNapMilli),
		nullFloat(e.RespiratoryRate), nullFloat(e.PerformancePct),
		nullFloat(e.ConsistencyPct), nullFloat(e.EfficiencyPct),
		now, now,
	)
	return err
}

// ListRange returns the user's rows with since <= date <= until, both bounds
// inclusive and either "" for an open bound, newest first.
func (r *SQLiteRepository) ListRange(ctx context.Context, userID, since, until string) ([]Entry, error) {
	args := []any{userID}
	clauses := []string{"user_id = ?"}
	if since != "" {
		clauses = append(clauses, "date >= ?")
		args = append(args, since)
	}
	if until != "" {
		clauses = append(clauses, "date <= ?")
		args = append(args, until)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+sleepColumns+`
		FROM user_whoop_sleep
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY date DESC, ended_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Latest returns the user's most recent row. No rows is not an error — a
// freshly connected or under-scoped account has none — so it returns
// (nil, nil) on sql.ErrNoRows and lets callers distinguish "not synced" from a
// real failure.
func (r *SQLiteRepository) Latest(ctx context.Context, userID string) (*Entry, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+sleepColumns+`
		FROM user_whoop_sleep
		WHERE user_id = ?
		ORDER BY date DESC, ended_at DESC
		LIMIT 1`, userID)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("whoopsleep: latest: %w", err)
	}
	return &e, nil
}

// CountForUser returns how many sleep rows the user has stored.
func (r *SQLiteRepository) CountForUser(ctx context.Context, userID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_whoop_sleep WHERE user_id = ?
	`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("whoopsleep: count for user: %w", err)
	}
	return n, nil
}

// DeleteByWhoopSleepID hard-deletes the user's row with the matching WHOOP
// sleep UUID. A missing row is not an error (idempotent webhook delete).
func (r *SQLiteRepository) DeleteByWhoopSleepID(ctx context.Context, userID, whoopSleepID string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_whoop_sleep
		WHERE user_id = ? AND whoop_sleep_id = ?
	`, userID, whoopSleepID)
	return err
}

// DeleteForUser hard-deletes every sleep row belonging to the user.
func (r *SQLiteRepository) DeleteForUser(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_whoop_sleep WHERE user_id = ?
	`, userID)
	return err
}

// nullInt maps a *int64 to a sql.NullInt64 so a nil pointer stores SQL NULL
// rather than a zero value — the difference between "WHOOP scored zero awake
// time" and "WHOOP has not scored this yet".
func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// nullFloat is nullInt's float twin; see whooprecovery for the same helper.
func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// scanner is satisfied by *sql.Row and *sql.Rows; lets the same scan path
// service both single-row reads and multi-row List loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(s scanner) (Entry, error) {
	var (
		e                                       Entry
		inBed, awake, noData, light, slowWave   sql.NullInt64
		rem, cycles, disturbances               sql.NullInt64
		needBase, needDebt, needStrain, needNap sql.NullInt64
		respRate, performance, consistency      sql.NullFloat64
		efficiency                              sql.NullFloat64
	)
	if err := s.Scan(
		&e.ID, &e.UserID, &e.WhoopSleepID, &e.Date, &e.IsNap, &e.StartedAt, &e.EndedAt,
		&e.TimezoneOffset, &e.ScoreState,
		&inBed, &awake, &noData, &light, &slowWave, &rem, &cycles, &disturbances,
		&needBase, &needDebt, &needStrain, &needNap,
		&respRate, &performance, &consistency, &efficiency,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return Entry{}, err
	}
	e.InBedMilli = intOrNil(inBed)
	e.AwakeMilli = intOrNil(awake)
	e.NoDataMilli = intOrNil(noData)
	e.LightSleepMilli = intOrNil(light)
	e.SlowWaveSleepMilli = intOrNil(slowWave)
	e.REMSleepMilli = intOrNil(rem)
	e.SleepCycleCount = intOrNil(cycles)
	e.DisturbanceCount = intOrNil(disturbances)
	e.NeedBaselineMilli = intOrNil(needBase)
	e.NeedFromSleepDebtMilli = intOrNil(needDebt)
	e.NeedFromStrainMilli = intOrNil(needStrain)
	e.NeedFromNapMilli = intOrNil(needNap)
	e.RespiratoryRate = floatOrNil(respRate)
	e.PerformancePct = floatOrNil(performance)
	e.ConsistencyPct = floatOrNil(consistency)
	e.EfficiencyPct = floatOrNil(efficiency)
	return e, nil
}

// intOrNil / floatOrNil exist because there are sixteen nullable columns here
// against whooprecovery's three: open-coding the if-Valid dance sixteen times
// is where a copy-paste lands one column's value in another's field.
func intOrNil(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func floatOrNil(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}
