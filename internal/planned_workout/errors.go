package plannedworkout

import "errors"

var (
	// ErrNotFound is returned when a planned workout does not exist, was
	// soft-deleted, or belongs to a different user. Cross-user lookups
	// surface as ErrNotFound (not Forbidden) so a probing client can't
	// distinguish "exists for someone else" from "does not exist."
	ErrNotFound = errors.New("plannedworkout: not found")

	// ErrUserRequired is returned when UserID is empty. Schema enforces
	// NOT NULL as well; the handler-side validation gives a clean 400.
	ErrUserRequired = errors.New("plannedworkout: user_id is required")

	// ErrInvalidActivityKind is returned when activity_kind isn't "lift" or
	// "run". The schema CHECK enforces the same set; surface as 400, not 500.
	ErrInvalidActivityKind = errors.New("plannedworkout: activity_kind must be 'lift' or 'run'")

	// ErrAgendaKindMismatch is returned when the agenda doesn't match the
	// activity kind: a lift carrying run fields, or a run carrying exercises.
	ErrAgendaKindMismatch = errors.New("plannedworkout: agenda does not match activity_kind")

	// ErrInvalidRunType is returned when run_type is set but isn't one of
	// easy/threshold/intervals.
	ErrInvalidRunType = errors.New("plannedworkout: run_type must be 'easy', 'threshold', or 'intervals'")

	// ErrInvalidWindow is returned when either scheduled boundary is the
	// zero time or end is not strictly after start.
	ErrInvalidWindow = errors.New("plannedworkout: scheduled end must be after start")

	// ErrInvalidTimezone is returned when timezone is empty or not a
	// time.LoadLocation-resolvable IANA name. The server writes Google
	// Calendar events in this zone, so a bad value can't be tolerated.
	ErrInvalidTimezone = errors.New("plannedworkout: timezone must be a valid IANA name")

	// ErrInvalidStatus is returned when status isn't one of
	// planned/completed/skipped. Schema CHECK enforces this as well.
	ErrInvalidStatus = errors.New("plannedworkout: status must be 'planned', 'completed', or 'skipped'")

	// ErrInvalidCalendarDetail is returned when calendar_detail is set but
	// isn't time_block/full_agenda.
	ErrInvalidCalendarDetail = errors.New("plannedworkout: calendar_detail must be 'time_block' or 'full_agenda'")

	// ErrInvalidCompletionLink is returned when the completion link is
	// half-populated (only one of session id / kind set) or the kind isn't
	// workout/activity. The two columns are written together or not at all.
	ErrInvalidCompletionLink = errors.New("plannedworkout: completion link requires both session id and a valid kind")

	// ErrInvalidExercise is returned when a planned exercise is missing its
	// exercise_id.
	ErrInvalidExercise = errors.New("plannedworkout: exercise_id is required")

	// ErrInvalidSet is returned for a malformed planned set: a unit other
	// than lb/kg, or a non-positive target reps / target weight.
	ErrInvalidSet = errors.New("plannedworkout: invalid planned set")

	// ErrInvalidRPE is returned when target_rpe is set but outside [1,10].
	ErrInvalidRPE = errors.New("plannedworkout: target_rpe must be between 1 and 10")

	// ErrCalendarNotConnected is returned by the calendar scheduler when a sync
	// is attempted but the user has no calendar connection. The handler maps it
	// to a 409 prompting the user to connect first. Declared here (not in the
	// calendarsync package) so the handler can errors.Is it without importing
	// calendarsync — that package already imports plannedworkout, so putting the
	// sentinel here avoids an import cycle while letting the service reuse it.
	ErrCalendarNotConnected = errors.New("plannedworkout: calendar not connected")

	// ErrCalendarReconnectNeeded is returned when the calendar grant is no
	// longer usable (revoked, or Google rejected the token). The handler maps it
	// to a 409 prompting re-consent. Same import-cycle rationale as
	// ErrCalendarNotConnected.
	ErrCalendarReconnectNeeded = errors.New("plannedworkout: calendar reconnect needed")
)

// isValidationError reports whether err is one of the package's clean-400
// validation sentinels (as opposed to ErrNotFound or a storage error). The
// handler (Task 3) uses this to map Validate failures to 400.
func isValidationError(err error) bool {
	switch {
	case errors.Is(err, ErrUserRequired),
		errors.Is(err, ErrInvalidActivityKind),
		errors.Is(err, ErrAgendaKindMismatch),
		errors.Is(err, ErrInvalidRunType),
		errors.Is(err, ErrInvalidWindow),
		errors.Is(err, ErrInvalidTimezone),
		errors.Is(err, ErrInvalidStatus),
		errors.Is(err, ErrInvalidCalendarDetail),
		errors.Is(err, ErrInvalidCompletionLink),
		errors.Is(err, ErrInvalidExercise),
		errors.Is(err, ErrInvalidSet),
		errors.Is(err, ErrInvalidRPE):
		return true
	}
	return false
}
