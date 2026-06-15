package plannedworkout

import (
	"errors"
	"testing"
	"time"
)

func validPlan() *PlannedWorkout {
	start := time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC)
	return &PlannedWorkout{
		UserID:            "u1",
		ActivityKind:      ActivityKindLift,
		ScheduledStartUTC: start,
		ScheduledEndUTC:   start.Add(time.Hour),
		Timezone:          "America/New_York",
		Status:            StatusPlanned,
	}
}

func TestValidate_OK(t *testing.T) {
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	// Empty status is rejected by Validate (per spec status must be one of
	// planned/completed/skipped); the repo defaults it to planned BEFORE
	// calling Validate, so callers never hit this through the repo.
	pw := validPlan()
	pw.Status = ""
	if err := pw.Validate(); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("empty status should fail Validate with ErrInvalidStatus, got %v", err)
	}
}

func TestValidate_UserRequired(t *testing.T) {
	pw := validPlan()
	pw.UserID = ""
	if err := pw.Validate(); !errors.Is(err, ErrUserRequired) {
		t.Errorf("want ErrUserRequired, got %v", err)
	}
}

func TestValidate_ActivityKind(t *testing.T) {
	pw := validPlan()
	pw.ActivityKind = "run"
	if err := pw.Validate(); !errors.Is(err, ErrInvalidActivityKind) {
		t.Errorf("want ErrInvalidActivityKind, got %v", err)
	}
}

func TestValidate_Window(t *testing.T) {
	t.Run("zero start", func(t *testing.T) {
		pw := validPlan()
		pw.ScheduledStartUTC = time.Time{}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("want ErrInvalidWindow, got %v", err)
		}
	})
	t.Run("zero end", func(t *testing.T) {
		pw := validPlan()
		pw.ScheduledEndUTC = time.Time{}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("want ErrInvalidWindow, got %v", err)
		}
	})
	t.Run("end not after start", func(t *testing.T) {
		pw := validPlan()
		pw.ScheduledEndUTC = pw.ScheduledStartUTC
		if err := pw.Validate(); !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("want ErrInvalidWindow, got %v", err)
		}
	})
}

func TestValidate_Timezone(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		pw := validPlan()
		pw.Timezone = ""
		if err := pw.Validate(); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("want ErrInvalidTimezone, got %v", err)
		}
	})
	t.Run("bogus", func(t *testing.T) {
		pw := validPlan()
		pw.Timezone = "Mars/Phobos"
		if err := pw.Validate(); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("want ErrInvalidTimezone, got %v", err)
		}
	})
}

func TestValidate_Status(t *testing.T) {
	pw := validPlan()
	pw.Status = "in_progress"
	if err := pw.Validate(); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("want ErrInvalidStatus, got %v", err)
	}
}

func TestValidate_CalendarDetail(t *testing.T) {
	pw := validPlan()
	bad := CalendarDetail("verbose")
	pw.CalendarDetail = &bad
	if err := pw.Validate(); !errors.Is(err, ErrInvalidCalendarDetail) {
		t.Errorf("want ErrInvalidCalendarDetail, got %v", err)
	}
}

func TestValidate_CompletionLink(t *testing.T) {
	t.Run("id without kind", func(t *testing.T) {
		pw := validPlan()
		pw.CompletedSessionID = ptrStr("s1")
		if err := pw.Validate(); !errors.Is(err, ErrInvalidCompletionLink) {
			t.Errorf("want ErrInvalidCompletionLink, got %v", err)
		}
	})
	t.Run("kind without id", func(t *testing.T) {
		pw := validPlan()
		pw.CompletedSessionKind = ptrKind(SessionKindWorkout)
		if err := pw.Validate(); !errors.Is(err, ErrInvalidCompletionLink) {
			t.Errorf("want ErrInvalidCompletionLink, got %v", err)
		}
	})
	t.Run("bad kind", func(t *testing.T) {
		pw := validPlan()
		pw.CompletedSessionID = ptrStr("s1")
		bad := SessionKind("nap")
		pw.CompletedSessionKind = &bad
		if err := pw.Validate(); !errors.Is(err, ErrInvalidCompletionLink) {
			t.Errorf("want ErrInvalidCompletionLink, got %v", err)
		}
	})
	t.Run("both set valid", func(t *testing.T) {
		pw := validPlan()
		pw.Status = StatusCompleted
		pw.CompletedSessionID = ptrStr("s1")
		pw.CompletedSessionKind = ptrKind(SessionKindActivity)
		if err := pw.Validate(); err != nil {
			t.Errorf("valid completion link rejected: %v", err)
		}
	})
}

func TestValidate_ExerciseAndSets(t *testing.T) {
	t.Run("missing exercise id", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: ""}}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidExercise) {
			t.Errorf("want ErrInvalidExercise, got %v", err)
		}
	})
	t.Run("bad unit", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: "bench", Sets: []PlannedSet{{Unit: ptrStr("stone")}}}}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidSet) {
			t.Errorf("want ErrInvalidSet, got %v", err)
		}
	})
	t.Run("rpe out of range", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: "bench", Sets: []PlannedSet{{TargetRPE: ptrF(11)}}}}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidRPE) {
			t.Errorf("want ErrInvalidRPE, got %v", err)
		}
	})
	t.Run("non-positive reps", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: "bench", Sets: []PlannedSet{{TargetReps: ptrInt(0)}}}}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidSet) {
			t.Errorf("want ErrInvalidSet, got %v", err)
		}
	})
	t.Run("non-positive weight", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: "bench", Sets: []PlannedSet{{TargetWeight: ptrF(-5)}}}}
		if err := pw.Validate(); !errors.Is(err, ErrInvalidSet) {
			t.Errorf("want ErrInvalidSet, got %v", err)
		}
	})
	t.Run("valid agenda", func(t *testing.T) {
		pw := validPlan()
		pw.Exercises = []PlannedExercise{{ExerciseID: "bench", Sets: []PlannedSet{
			{TargetReps: ptrInt(5), TargetWeight: ptrF(185), Unit: ptrStr("lb"), TargetRPE: ptrF(8)},
		}}}
		if err := pw.Validate(); err != nil {
			t.Errorf("valid agenda rejected: %v", err)
		}
	})
}

func TestIsValidationError(t *testing.T) {
	if !isValidationError(ErrInvalidWindow) {
		t.Error("ErrInvalidWindow should be a validation error")
	}
	if isValidationError(ErrNotFound) {
		t.Error("ErrNotFound should not be a validation error")
	}
}
