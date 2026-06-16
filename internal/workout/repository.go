package workout

import (
	"context"
	"time"
)

// Repository persists workouts. Implementations may be backed by
// in-memory storage, SQLite, Postgres, etc. The interface is defined
// in domain language — callers should not need to know how workouts
// are stored.
type Repository interface {
	// Create persists a new workout. The implementation is responsible
	// for setting ID, CreatedAt, and UpdatedAt; callers should leave
	// these zero-valued.
	Create(ctx context.Context, w *Workout) error

	// GetByID returns a workout by its ID. Returns ErrNotFound if no
	// workout exists with that ID, or if it has been soft-deleted.
	GetByID(ctx context.Context, id string) (*Workout, error)

	// ListByUser returns workouts for a user, most recent first.
	// Soft-deleted workouts are excluded.
	ListByUser(ctx context.Context, userID string, opts ListOptions) ([]Workout, error)

	// CountByUser returns the total number of non-soft-deleted workouts
	// for the user, honoring only the Since/Until filters on opts —
	// Limit and Offset are ignored. Used to power pagination metadata
	// on the list endpoint ("showing N–M of TOTAL").
	CountByUser(ctx context.Context, userID string, opts ListOptions) (int, error)

	// Update replaces an existing workout. Returns ErrNotFound if the
	// workout doesn't exist or is soft-deleted.
	Update(ctx context.Context, w *Workout) error

	// Delete soft-deletes a workout by setting DeletedAt.
	// Returns ErrNotFound if the workout doesn't exist or is already deleted.
	Delete(ctx context.Context, id string) error

	// ListOneRepMaxHistory returns the user's per-workout estimated 1RM
	// entries for a single exercise, sorted most recent first. Optional
	// since/until bounds filter on performed_at. Returns an empty slice
	// when the user has no entries for the exercise.
	//
	// This is the read side of the exercise_one_rep_max_history table;
	// see prog-strength-docs/sows/estimated-one-rep-max.md
	// for design rationale. Pair with RecencyWeightedBaseline to compute
	// the user's current capability on the exercise.
	ListOneRepMaxHistory(ctx context.Context, userID, exerciseID string, since, until *time.Time) ([]OneRepMaxEntry, error)

	// ListPersonalRecords returns the authed user's personal records,
	// sorted by achieved_at DESC. Empty slice when the user has none.
	// See prog-strength-docs/sows/personal-records.md.
	ListPersonalRecords(ctx context.Context, userID string) ([]PersonalRecord, error)

	// ListPersonalRecordEventsByWorkouts returns every PR break event
	// whose workout_id is in the given slice. Empty input returns an
	// empty slice. Used by the workout list endpoint to embed
	// `personal_records_set` per workout in a single bulk query.
	ListPersonalRecordEventsByWorkouts(ctx context.Context, workoutIDs []string) ([]PersonalRecordEvent, error)

	// GetPersonalRecordEventsByIDs returns the PR break events whose id is
	// in the given slice. Empty input returns an empty slice. Used by the
	// timeline hydrator to render `pr` posts (source_id = event id) in a
	// single batch read rather than one query per post.
	GetPersonalRecordEventsByIDs(ctx context.Context, ids []string) ([]PersonalRecordEvent, error)

	// ListUserHeadlineExercises returns the user's custom headline-
	// exercise selection in display order (position ASC). Empty slice
	// means the user has never customized — callers fall back to
	// workout.HeadlineExercises to drive the Personal Records page.
	// See prog-strength-docs/sows/custom-headline-lifts.md.
	ListUserHeadlineExercises(ctx context.Context, userID string) ([]UserHeadlineExercise, error)

	// ReplaceUserHeadlineExercises atomically replaces the user's
	// headline-exercise selection with the given ordered slice of
	// exercise slugs. `position` is assigned from the slice index;
	// the implementation deletes the user's existing rows and inserts
	// the new set inside a single transaction. Caller is responsible
	// for validating that every slug exists in the exercise catalog
	// and that the slice respects MaxHeadlineExercises — the repo
	// trusts the input it's given.
	ReplaceUserHeadlineExercises(ctx context.Context, userID string, exerciseIDs []string, now time.Time) error

	// ListCompletedSessionsSince returns (PerformedAt, EndedAt) for the
	// user's non-deleted workouts that HAVE an EndedAt occurring at/after
	// `since`. End-less workouts are excluded — their duration is unknown,
	// so they can't contribute a session-minutes value. This is the raw
	// projection for the weekly profile-stats series; bucketing into local
	// weeks happens in the handler (a "week" is a user-local concept SQLite
	// date() can't bucket correctly across DST).
	ListCompletedSessionsSince(ctx context.Context, userID string, since time.Time) ([]SessionDuration, error)
}

// SessionDuration is the minimal (start, end) projection for one completed
// workout, used to compute weekly lift-session minutes in the profile-stats
// handler. Only workouts with a non-nil EndedAt are returned.
type SessionDuration struct {
	PerformedAt time.Time
	EndedAt     time.Time
}

// ListOptions controls pagination and filtering for list operations.
type ListOptions struct {
	Limit  int        // 0 means use a sensible default (e.g., 50)
	Offset int        // for pagination
	Since  *time.Time // only workouts performed at or after this time
	Until  *time.Time // only workouts performed at or before this time
}
