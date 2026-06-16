// Package plannedworkout models forward-looking training entries that pair
// a scheduled time window with an optional lift agenda and Google Calendar
// sync bookkeeping. See prog-strength-docs SOW "Planned Workouts & Google
// Calendar Sync".
//
// A planned workout is the future-facing counterpart to a logged workout:
// it carries a UTC window plus the user's timezone (so the server can write
// Google Calendar events in the right zone), a lifecycle status, an optional
// agenda (exercises → sets with target reps/weight/RPE), and a link to the
// session that fulfilled it once completed.
package plannedworkout

import "time"

// Status is the lifecycle of a planned workout.
type Status string

const (
	StatusPlanned   Status = "planned"
	StatusCompleted Status = "completed"
	StatusSkipped   Status = "skipped"
)

// ActivityKind is the kind of training the plan represents. A "lift" carries
// an exercise agenda (Exercises); a "run" carries a RunType + free-text
// RunDetails. Both kinds' details are optional, so either can be a bare
// time block.
type ActivityKind string

const (
	ActivityKindLift ActivityKind = "lift"
	ActivityKindRun  ActivityKind = "run"
)

// RunType is the kind of run a planned run represents. Optional — a run plan
// can omit it and just block time (or describe everything in RunDetails).
type RunType string

const (
	RunTypeEasy      RunType = "easy"
	RunTypeThreshold RunType = "threshold"
	RunTypeIntervals RunType = "intervals"
)

// SessionKind distinguishes the two session tables a completed plan can
// point at: a strength workout or a cardio/other activity.
type SessionKind string

const (
	SessionKindWorkout  SessionKind = "workout"
	SessionKindActivity SessionKind = "activity"
)

// CalendarDetail controls how much of the agenda is written into the Google
// Calendar event body.
type CalendarDetail string

const (
	DetailTimeBlock  CalendarDetail = "time_block"
	DetailFullAgenda CalendarDetail = "full_agenda"
)

// GoogleSyncStatus tracks the state of the most recent Google Calendar sync
// attempt for a plan.
type GoogleSyncStatus string

const (
	SyncPending GoogleSyncStatus = "pending"
	SyncSynced  GoogleSyncStatus = "synced"
	SyncFailed  GoogleSyncStatus = "failed"
)

// PlannedSet is one target set within a planned exercise. All target fields
// are optional pointers so "do 3 sets, figure out the weight later" is
// expressible. Unit is denormalized per row for the same reason as
// bodyweight: a unit-preference change must not reinterpret saved targets.
type PlannedSet struct {
	ID           string
	OrderIndex   int
	TargetReps   *int
	TargetWeight *float64
	Unit         *string
	TargetRPE    *float64
	// AMRAP ("as many reps as possible") marks a set with no fixed rep target
	// — the lifter goes to the limit. When true, TargetReps is ignored for
	// display.
	AMRAP bool
}

// PlannedExercise is one exercise in a plan's agenda, carrying its ordered
// target sets.
type PlannedExercise struct {
	ID         string
	ExerciseID string
	OrderIndex int
	Notes      *string
	// SupersetGroup, when non-nil, groups exercises performed as a superset
	// (alternating sets). Exercises sharing the same value belong to the same
	// superset; nil is a standalone exercise. Mirrors the logged-workout model
	// (workout.WorkoutExercise.SupersetGroup).
	SupersetGroup *int
	Sets          []PlannedSet
}

// PlannedWorkout is a scheduled training entry plus its optional agenda and
// Google Calendar bookkeeping. The schedule is stored in UTC; Timezone is
// the IANA zone the server uses when writing the calendar event.
type PlannedWorkout struct {
	ID           string
	UserID       string
	Name         *string
	ActivityKind ActivityKind

	ScheduledStartUTC time.Time
	ScheduledEndUTC   time.Time
	Timezone          string

	Status Status
	Notes  *string

	CompletedSessionID   *string
	CompletedSessionKind *SessionKind

	CalendarDetail *CalendarDetail

	GoogleEventID    *string
	GoogleSyncStatus *GoogleSyncStatus
	LastSyncError    *string

	// Run agenda. Set only for ActivityKindRun; nil/empty for lifts.
	RunType    *RunType
	RunDetails *string

	Exercises []PlannedExercise

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// Validate enforces the shape invariants the schema's CHECK / NOT NULL
// constraints also enforce, plus the cross-field rules SQLite can't express
// (window ordering, IANA timezone resolvability, the all-or-nothing
// completion link). Run handler-side so the caller gets a clean 400 instead
// of a 500 from a constraint violation.
func (pw *PlannedWorkout) Validate() error {
	if pw.UserID == "" {
		return ErrUserRequired
	}
	switch pw.ActivityKind {
	case ActivityKindLift, ActivityKindRun:
	default:
		return ErrInvalidActivityKind
	}
	// Agenda must match the kind: a lift carries exercises (not run fields),
	// a run carries run_type/run_details (not exercises). Both are optional,
	// so the empty case is valid for either kind.
	if pw.ActivityKind == ActivityKindLift {
		if pw.RunType != nil || pw.RunDetails != nil {
			return ErrAgendaKindMismatch
		}
	}
	if pw.ActivityKind == ActivityKindRun {
		if len(pw.Exercises) > 0 {
			return ErrAgendaKindMismatch
		}
		if pw.RunType != nil {
			switch *pw.RunType {
			case RunTypeEasy, RunTypeThreshold, RunTypeIntervals:
			default:
				return ErrInvalidRunType
			}
		}
	}
	if pw.ScheduledStartUTC.IsZero() || pw.ScheduledEndUTC.IsZero() ||
		!pw.ScheduledEndUTC.After(pw.ScheduledStartUTC) {
		return ErrInvalidWindow
	}
	if pw.Timezone == "" {
		return ErrInvalidTimezone
	}
	if _, err := time.LoadLocation(pw.Timezone); err != nil {
		return ErrInvalidTimezone
	}
	switch pw.Status {
	case StatusPlanned, StatusCompleted, StatusSkipped:
	default:
		return ErrInvalidStatus
	}
	if pw.CalendarDetail != nil {
		switch *pw.CalendarDetail {
		case DetailTimeBlock, DetailFullAgenda:
		default:
			return ErrInvalidCalendarDetail
		}
	}
	// Completion link is all-or-nothing: either both id and kind are set
	// (with a valid kind) or neither is.
	if pw.CompletedSessionID != nil || pw.CompletedSessionKind != nil {
		if pw.CompletedSessionID == nil || pw.CompletedSessionKind == nil {
			return ErrInvalidCompletionLink
		}
		switch *pw.CompletedSessionKind {
		case SessionKindWorkout, SessionKindActivity:
		default:
			return ErrInvalidCompletionLink
		}
	}
	for _, ex := range pw.Exercises {
		if ex.ExerciseID == "" {
			return ErrInvalidExercise
		}
		for _, s := range ex.Sets {
			if s.Unit != nil && *s.Unit != "lb" && *s.Unit != "kg" {
				return ErrInvalidSet
			}
			if s.TargetRPE != nil && (*s.TargetRPE < 1 || *s.TargetRPE > 10) {
				return ErrInvalidRPE
			}
			if s.TargetReps != nil && *s.TargetReps <= 0 {
				return ErrInvalidSet
			}
			if s.TargetWeight != nil && *s.TargetWeight <= 0 {
				return ErrInvalidSet
			}
		}
	}
	return nil
}
