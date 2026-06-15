package plannedworkout

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// planColumns is the canonical select list for planned_workouts, kept in one
// place so the scan order can't drift between Get and List.
const planColumns = `
	id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
	timezone, status, notes, completed_session_id, completed_session_kind,
	calendar_detail, google_event_id, google_sync_status, last_sync_error,
	created_at, updated_at, deleted_at`

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

// Create inserts the plan and its full agenda in one transaction. The
// implementation stamps ID, CreatedAt, and UpdatedAt; Status defaults to
// "planned" when empty. Validation runs first so a bad payload never opens a
// transaction.
func (r *SQLiteRepository) Create(ctx context.Context, pw *PlannedWorkout) error {
	if pw.Status == "" {
		pw.Status = StatusPlanned
	}
	if err := pw.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	pw.ID = id.New()
	pw.CreatedAt = now
	pw.UpdatedAt = now
	pw.DeletedAt = nil

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO planned_workouts (
			id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, notes, completed_session_id, completed_session_kind,
			calendar_detail, google_event_id, google_sync_status, last_sync_error,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		pw.ID, pw.UserID, pw.Name, string(pw.ActivityKind),
		pw.ScheduledStartUTC, pw.ScheduledEndUTC, pw.Timezone, string(pw.Status),
		pw.Notes, pw.CompletedSessionID, kindStr(pw.CompletedSessionKind),
		detailStr(pw.CalendarDetail), pw.GoogleEventID, syncStr(pw.GoogleSyncStatus),
		pw.LastSyncError, pw.CreatedAt, pw.UpdatedAt,
	); err != nil {
		return err
	}

	if err := insertAgendaTx(ctx, tx, pw); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLiteRepository) Get(ctx context.Context, userID, planID string) (*PlannedWorkout, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+planColumns+`
		FROM planned_workouts
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, planID, userID)
	pw, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := r.hydrateAgenda(ctx, pw); err != nil {
		return nil, err
	}
	return pw, nil
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, since, until *time.Time) ([]PlannedWorkout, error) {
	args := []any{userID}
	clauses := []string{"user_id = ?", "deleted_at IS NULL"}
	if since != nil {
		clauses = append(clauses, "scheduled_start_utc >= ?")
		args = append(args, *since)
	}
	if until != nil {
		clauses = append(clauses, "scheduled_start_utc < ?")
		args = append(args, *until)
	}
	q := `
		SELECT ` + planColumns + `
		FROM planned_workouts
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY scheduled_start_utc ASC
	`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PlannedWorkout
	for rows.Next() {
		pw, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate after the rows cursor is closed so the per-plan agenda queries
	// don't contend with the open List cursor on the single writer conn.
	for i := range out {
		if err := r.hydrateAgenda(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Update overwrites the plan's mutable fields and replaces its agenda in one
// transaction. created_at is preserved (re-read from the existing row);
// updated_at bumps. Validation runs first.
func (r *SQLiteRepository) Update(ctx context.Context, pw *PlannedWorkout) error {
	if pw.Status == "" {
		pw.Status = StatusPlanned
	}
	if err := pw.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE planned_workouts SET
			name = ?, activity_kind = ?, scheduled_start_utc = ?, scheduled_end_utc = ?,
			timezone = ?, status = ?, notes = ?, completed_session_id = ?,
			completed_session_kind = ?, calendar_detail = ?, google_event_id = ?,
			google_sync_status = ?, last_sync_error = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`,
		pw.Name, string(pw.ActivityKind), pw.ScheduledStartUTC, pw.ScheduledEndUTC,
		pw.Timezone, string(pw.Status), pw.Notes, pw.CompletedSessionID,
		kindStr(pw.CompletedSessionKind), detailStr(pw.CalendarDetail), pw.GoogleEventID,
		syncStr(pw.GoogleSyncStatus), pw.LastSyncError, now,
		pw.ID, pw.UserID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	// Replace the agenda. Foreign keys are ON (see db.Open), so deleting the
	// exercise rows cascades to planned_sets; then reinsert with fresh ids.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM planned_workout_exercises WHERE planned_workout_id = ?
	`, pw.ID); err != nil {
		return err
	}
	if err := insertAgendaTx(ctx, tx, pw); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	pw.UpdatedAt = now
	return nil
}

func (r *SQLiteRepository) Delete(ctx context.Context, userID, planID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE planned_workouts SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, now, planID, userID)
	return affectedOrNotFound(res, err)
}

func (r *SQLiteRepository) SetStatus(ctx context.Context, userID, planID string, status Status) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE planned_workouts SET status = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, string(status), now, planID, userID)
	return affectedOrNotFound(res, err)
}

func (r *SQLiteRepository) SetCompletion(ctx context.Context, userID, planID, sessionID string, kind SessionKind) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE planned_workouts SET
			status = ?, completed_session_id = ?, completed_session_kind = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, string(StatusCompleted), sessionID, string(kind), now, planID, userID)
	return affectedOrNotFound(res, err)
}

func (r *SQLiteRepository) SetGoogleSync(ctx context.Context, userID, planID string, eventID *string, status GoogleSyncStatus, lastErr *string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE planned_workouts SET
			google_event_id = ?, google_sync_status = ?, last_sync_error = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, eventID, string(status), lastErr, now, planID, userID)
	return affectedOrNotFound(res, err)
}

// insertAgendaTx inserts the plan's exercises and their sets, stamping a
// fresh id and order_index = slice position on each. Run inside Create's and
// Update's transactions.
func insertAgendaTx(ctx context.Context, tx *sql.Tx, pw *PlannedWorkout) error {
	for i := range pw.Exercises {
		ex := &pw.Exercises[i]
		ex.ID = id.New()
		ex.OrderIndex = i
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO planned_workout_exercises (
				id, planned_workout_id, exercise_id, order_index, notes
			) VALUES (?, ?, ?, ?, ?)
		`, ex.ID, pw.ID, ex.ExerciseID, ex.OrderIndex, ex.Notes); err != nil {
			return err
		}
		for j := range ex.Sets {
			s := &ex.Sets[j]
			s.ID = id.New()
			s.OrderIndex = j
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO planned_sets (
					id, planned_workout_exercise_id, order_index,
					target_reps, target_weight, unit, target_rpe
				) VALUES (?, ?, ?, ?, ?, ?, ?)
			`, s.ID, ex.ID, s.OrderIndex, s.TargetReps, s.TargetWeight, s.Unit, s.TargetRPE); err != nil {
				return err
			}
		}
	}
	return nil
}

// hydrateAgenda loads the plan's exercises (ordered) and each exercise's sets
// (ordered) via secondary queries.
func (r *SQLiteRepository) hydrateAgenda(ctx context.Context, pw *PlannedWorkout) error {
	exRows, err := r.db.QueryContext(ctx, `
		SELECT id, exercise_id, order_index, notes
		FROM planned_workout_exercises
		WHERE planned_workout_id = ?
		ORDER BY order_index ASC
	`, pw.ID)
	if err != nil {
		return err
	}
	var exercises []PlannedExercise
	for exRows.Next() {
		var ex PlannedExercise
		var notes sql.NullString
		if err := exRows.Scan(&ex.ID, &ex.ExerciseID, &ex.OrderIndex, &notes); err != nil {
			exRows.Close()
			return err
		}
		if notes.Valid {
			ex.Notes = &notes.String
		}
		exercises = append(exercises, ex)
	}
	if err := exRows.Err(); err != nil {
		exRows.Close()
		return err
	}
	exRows.Close()

	for i := range exercises {
		sets, err := r.loadSets(ctx, exercises[i].ID)
		if err != nil {
			return err
		}
		exercises[i].Sets = sets
	}
	pw.Exercises = exercises
	return nil
}

func (r *SQLiteRepository) loadSets(ctx context.Context, exerciseID string) ([]PlannedSet, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, order_index, target_reps, target_weight, unit, target_rpe
		FROM planned_sets
		WHERE planned_workout_exercise_id = ?
		ORDER BY order_index ASC
	`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sets []PlannedSet
	for rows.Next() {
		var (
			s      PlannedSet
			reps   sql.NullInt64
			weight sql.NullFloat64
			unit   sql.NullString
			rpe    sql.NullFloat64
		)
		if err := rows.Scan(&s.ID, &s.OrderIndex, &reps, &weight, &unit, &rpe); err != nil {
			return nil, err
		}
		if reps.Valid {
			v := int(reps.Int64)
			s.TargetReps = &v
		}
		if weight.Valid {
			v := weight.Float64
			s.TargetWeight = &v
		}
		if unit.Valid {
			v := unit.String
			s.Unit = &v
		}
		if rpe.Valid {
			v := rpe.Float64
			s.TargetRPE = &v
		}
		sets = append(sets, s)
	}
	return sets, rows.Err()
}

// scanner is satisfied by *sql.Row and *sql.Rows; lets the same scan path
// service both single-row Get and multi-row List loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanPlan(s scanner) (*PlannedWorkout, error) {
	var (
		pw           PlannedWorkout
		activityKind string
		status       string
		name         sql.NullString
		notes        sql.NullString
		completedID  sql.NullString
		completedK   sql.NullString
		detail       sql.NullString
		eventID      sql.NullString
		syncStatus   sql.NullString
		lastErr      sql.NullString
		deletedAt    sql.NullTime
	)
	if err := s.Scan(
		&pw.ID, &pw.UserID, &name, &activityKind, &pw.ScheduledStartUTC, &pw.ScheduledEndUTC,
		&pw.Timezone, &status, &notes, &completedID, &completedK,
		&detail, &eventID, &syncStatus, &lastErr,
		&pw.CreatedAt, &pw.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	pw.ActivityKind = ActivityKind(activityKind)
	pw.Status = Status(status)
	if name.Valid {
		pw.Name = &name.String
	}
	if notes.Valid {
		pw.Notes = &notes.String
	}
	if completedID.Valid {
		pw.CompletedSessionID = &completedID.String
	}
	if completedK.Valid {
		k := SessionKind(completedK.String)
		pw.CompletedSessionKind = &k
	}
	if detail.Valid {
		d := CalendarDetail(detail.String)
		pw.CalendarDetail = &d
	}
	if eventID.Valid {
		pw.GoogleEventID = &eventID.String
	}
	if syncStatus.Valid {
		st := GoogleSyncStatus(syncStatus.String)
		pw.GoogleSyncStatus = &st
	}
	if lastErr.Valid {
		pw.LastSyncError = &lastErr.String
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		pw.DeletedAt = &t
	}
	return &pw, nil
}

// affectedOrNotFound collapses a single-row mutation into ErrNotFound when no
// live, owned row matched.
func affectedOrNotFound(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// kindStr / detailStr / syncStr render nullable enum pointers as nil for a
// NULL column when the pointer is nil, else the string value.
func kindStr(k *SessionKind) any {
	if k == nil {
		return nil
	}
	return string(*k)
}

func detailStr(d *CalendarDetail) any {
	if d == nil {
		return nil
	}
	return string(*d)
}

func syncStr(s *GoogleSyncStatus) any {
	if s == nil {
		return nil
	}
	return string(*s)
}
