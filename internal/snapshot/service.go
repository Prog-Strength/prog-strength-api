package snapshot

import (
	"context"
	"log"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// Narrow consumer interfaces: only the methods the snapshot needs, so
// tests can supply tiny fakes. The concrete domain repositories satisfy
// these. (Idiomatic Go: accept the interface you use.)
type workoutReader interface {
	ListByUser(ctx context.Context, userID string, opts workout.ListOptions) ([]workout.Workout, error)
	ListPersonalRecordEventsByWorkouts(ctx context.Context, workoutIDs []string) ([]workout.PersonalRecordEvent, error)
}

type exerciseReader interface {
	List(ctx context.Context, opts exercise.ListOptions) ([]exercise.Exercise, error)
}

type activityReader interface {
	ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]activity.Activity, error)
	GetUserRunningBestEfforts(ctx context.Context, userID string) ([]activity.RunningBestEffort, error)
}

type stepsReader interface {
	List(ctx context.Context, userID string, since, until *string, limit int, before *string) ([]steps.Entry, string, error)
	GetGoal(ctx context.Context, userID string) (steps.Goal, error)
}

type bodyweightReader interface {
	List(ctx context.Context, userID string, since, until *time.Time) ([]bodyweight.Entry, error)
}

type nutritionReader interface {
	DailyMacros(ctx context.Context, userID string, since, until time.Time, loc *time.Location) ([]nutrition.DailyMacros, error)
	GetMacroGoals(ctx context.Context, userID string) (nutrition.MacroGoals, error)
}

type userReader interface {
	GetByID(ctx context.Context, id string) (*user.User, error)
}

// Service composes the domain repositories into one snapshot. It holds
// only the narrow reader interfaces so a test can swap in fakes.
type Service struct {
	workoutRepo    workoutReader
	exerciseRepo   exerciseReader
	activityRepo   activityReader
	stepsRepo      stepsReader
	bodyweightRepo bodyweightReader
	nutritionRepo  nutritionReader
	userRepo       userReader
}

func NewService(w workoutReader, e exerciseReader, a activityReader, s stepsReader,
	b bodyweightReader, n nutritionReader, u userReader) *Service {
	return &Service{w, e, a, s, b, n, u}
}

// Build composes the snapshot over [start, end) (UTC half-open) localized
// by loc. Each domain section is read defensively: a repo error nulls that
// section only (logged with the request id), never the whole response.
func (s *Service) Build(ctx context.Context, userID string, start, end time.Time, loc *time.Location) Snapshot {
	days := countLocalDays(start, end, loc)
	snap := Snapshot{
		Period: Period{
			StartDate: start.In(loc).Format("2006-01-02"),
			EndDate:   end.In(loc).AddDate(0, 0, -1).Format("2006-01-02"),
			Timezone:  loc.String(),
			Days:      days,
		},
		Consistency: Consistency{WindowDays: days},
	}

	// Weight PRs and volumes are stored in the user's unit, never
	// converted; fall back to "lb" if the user read fails so the section
	// still labels its numbers.
	unit := string(user.WeightUnitPounds)
	if u, err := s.userRepo.GetByID(ctx, userID); err == nil && u != nil {
		unit = string(u.WeightUnit)
	}

	snap.Strength = sectionOrNil(ctx, "strength", func() (*StrengthSection, error) {
		ws, err := s.workoutRepo.ListByUser(ctx, userID, workout.ListOptions{Since: &start, Until: &end})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(ws))
		for i, w := range ws {
			ids[i] = w.ID
		}
		prs, err := s.workoutRepo.ListPersonalRecordEventsByWorkouts(ctx, ids)
		if err != nil {
			return nil, err
		}
		exs, err := s.exerciseRepo.List(ctx, exercise.ListOptions{})
		if err != nil {
			return nil, err
		}
		return aggregateStrength(ws, prs, exs, unit, loc), nil
	})

	snap.Running = sectionOrNil(ctx, "running", func() (*RunningSection, error) {
		acts, err := s.activityRepo.ListInRange(ctx, userID, &start, &end)
		if err != nil {
			return nil, err
		}
		bests, err := s.activityRepo.GetUserRunningBestEfforts(ctx, userID)
		if err != nil {
			return nil, err
		}
		return aggregateRunning(acts, bests, start, end, loc), nil
	})

	snap.Steps = sectionOrNil(ctx, "steps", func() (*StepsSection, error) {
		since := start.In(loc).Format("2006-01-02")
		until := end.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
		entries, _, err := s.stepsRepo.List(ctx, userID, &since, &until, 0, nil)
		if err != nil {
			return nil, err
		}
		goal, err := s.stepsRepo.GetGoal(ctx, userID)
		if err != nil {
			return nil, err
		}
		return aggregateSteps(entries, goal.Goal, loc), nil
	})

	snap.Bodyweight = sectionOrNil(ctx, "bodyweight", func() (*BodyweightSection, error) {
		entries, err := s.bodyweightRepo.List(ctx, userID, &start, &end)
		if err != nil {
			return nil, err
		}
		readings := make([]bodyweightReading, len(entries))
		for i, e := range entries {
			readings[i] = bodyweightReading{measuredAt: e.MeasuredAt.In(loc), weight: e.Weight, unit: string(e.Unit)}
		}
		return aggregateBodyweight(readings), nil
	})

	snap.Nutrition = sectionOrNil(ctx, "nutrition", func() (*NutritionSection, error) {
		days, err := s.nutritionRepo.DailyMacros(ctx, userID, start, end, loc)
		if err != nil {
			return nil, err
		}
		goals, err := s.nutritionRepo.GetMacroGoals(ctx, userID)
		if err != nil {
			return nil, err
		}
		return aggregateNutrition(days, goals), nil
	})

	snap.Consistency.ActiveDays = countActiveDays(snap.Strength, snap.Running, snap.Steps)
	return snap
}

// sectionOrNil runs fn and, on error, logs (tagged with op + request id)
// and returns nil — the section degrades to JSON null. Mirrors the
// dashboard handler's defer1 defensive wrapper.
func sectionOrNil[T any](ctx context.Context, op string, fn func() (*T, error)) *T {
	v, err := fn()
	if err != nil {
		log.Printf("snapshot: %s for %s: %v", op, requestid.FromContext(ctx), err)
		return nil
	}
	return v
}

// countLocalDays counts whole local calendar days in [start, end). DST-safe
// (steps one local day at a time rather than dividing by 24h).
func countLocalDays(start, end time.Time, loc *time.Location) int {
	d := start.In(loc)
	cur := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	n := 0
	for cur.Before(end) {
		n++
		cur = cur.AddDate(0, 0, 1)
	}
	return n
}
