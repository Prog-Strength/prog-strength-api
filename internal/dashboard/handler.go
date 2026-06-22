package dashboard

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/daterange"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// sparkLookbackWeeks is how far back the running/lifting reads pull. The
// sparklines only cover the last 8 weeks, but the streak walk needs up to a
// year of active dates, so a single 53-week pull serves both — one query per
// domain instead of two.
const sparkLookbackWeeks = 53

// Handler composes the dashboard summary from every domain's read repository.
// It owns no domain logic: each section is built by a pure builder (running.go,
// lifting.go, …) from data this handler fetches. The handler's job is fetching
// resiliently — a recoverable failure in one domain yields a nil section, never
// a 500 — and assembling the envelope.
type Handler struct {
	activityRepo   activity.Repository
	workoutRepo    workout.Repository
	exerciseRepo   exercise.Repository
	stepsRepo      steps.Repository
	nutritionRepo  nutrition.Repository
	bodyweightRepo bodyweight.Repository
	userRepo       user.Repository

	// now sources the current instant for all local-week/local-day bucketing.
	// It defaults to time.Now; tests override it to pin a fixed reference time so
	// week-boundary assertions don't flake on the real calendar.
	now func() time.Time
}

// NewHandler builds a dashboard Handler backed by the given read repositories.
func NewHandler(
	activityRepo activity.Repository,
	workoutRepo workout.Repository,
	exerciseRepo exercise.Repository,
	stepsRepo steps.Repository,
	nutritionRepo nutrition.Repository,
	bodyweightRepo bodyweight.Repository,
	userRepo user.Repository,
) *Handler {
	return &Handler{
		activityRepo:   activityRepo,
		workoutRepo:    workoutRepo,
		exerciseRepo:   exerciseRepo,
		stepsRepo:      stepsRepo,
		nutritionRepo:  nutritionRepo,
		bodyweightRepo: bodyweightRepo,
		userRepo:       userRepo,
		now:            time.Now,
	}
}

// Mount registers the dashboard route. Callers are expected to have already
// wrapped the router in auth.RequireUser — summary reads the user ID from
// context and assumes it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/dashboard/summary", h.summary)
}

// summary handles GET /dashboard/summary — the aggregate "command center" tile
// payload. Requires a `timezone` query param (IANA name) so every section's
// local-week / local-day bucketing matches the user's wall clock.
//
// Each section is fetched and built independently and defensively: a recoverable
// repo error in one domain logs and yields a nil section rather than failing the
// whole request, so a single flaky table can't blank the dashboard. The streak
// is always present (an empty streak is a real zero state) and is derived from
// whatever domain reads succeeded.
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	tz := r.URL.Query().Get("timezone")
	if tz == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid timezone "+tz)
		return
	}

	now := h.now()

	// The authed user must exist; failing to load them (for the lifting unit)
	// is a server fault, not a recoverable per-section miss.
	u, err := h.userRepo.GetByID(ctx, userID)
	if err != nil {
		httpresp.ServerError(w, ctx, "get user", err)
		return
	}
	unit := string(u.WeightUnit)

	// Single broad windows reused across sections. since53w covers both the
	// 8-week sparklines and the streak's year-long week walk in one query.
	since53w := now.AddDate(0, 0, -7*sparkLookbackWeeks)
	since8w := now.AddDate(0, 0, -56)
	todayStr := now.In(loc).Format("2006-01-02")
	since53wStr := now.In(loc).AddDate(0, 0, -7*sparkLookbackWeeks).Format("2006-01-02")

	// Shared reads: runs (running + streak) and workouts (lifting + streak) and
	// steps entries (steps + streak) are each fetched once and reused. They are
	// fetched defensively so a failure degrades to a nil/empty contribution.
	runs := defer1(ctx, r, "running activities", func() ([]activity.Activity, error) {
		return h.activityRepo.ListInRange(ctx, userID, &since53w, nil)
	})
	workouts := defer1(ctx, r, "workouts", func() ([]workout.Workout, error) {
		return h.workoutRepo.ListByUser(ctx, userID, workout.ListOptions{Since: &since53w})
	})
	stepEntries := defer1(ctx, r, "steps", func() ([]steps.Entry, error) {
		entries, _, err := h.stepsRepo.List(ctx, userID, &since53wStr, &todayStr, 0, nil)
		return entries, err
	})

	summary := Summary{
		Running:    h.buildRunningSection(ctx, r, userID, runs, now, loc),
		Lifting:    h.buildLiftingSection(ctx, r, userID, workouts, unit, now, loc),
		Steps:      h.buildStepsSection(ctx, r, userID, stepEntries, now, loc),
		Nutrition:  h.buildNutritionSection(ctx, r, userID, todayStr, loc),
		Bodyweight: h.buildBodyweightSection(ctx, r, userID, since8w),
		Streak:     buildStreak(streakDates(runs, workouts, stepEntries, loc), now, loc),
	}

	httpresp.OK(w, "dashboard summary", summary)
}

// buildRunningSection fetches the running metrics and assembles the tile from
// them plus the already-fetched 53-week run list. RunningMetrics failing alone
// still yields a tile (zeroed current-week) when there are runs; the section is
// nil only when there's no running data at all (the builder's own nil-on-empty).
func (h *Handler) buildRunningSection(ctx context.Context, r *http.Request, userID string, runs []activity.Activity, now time.Time, loc *time.Location) *RunningSection {
	metrics := defer1(ctx, r, "running metrics", func() (activity.Metrics, error) {
		return h.activityRepo.RunningMetrics(ctx, userID, now, loc)
	})
	return buildRunning(metrics, runs, now, loc)
}

// buildLiftingSection assembles the lifting tile: weekly volume + duration from
// the 53-week workout list, this-week PR count from the PR-event table, and the
// headline 1RM from the user's PRs. A failure computing the PR count or headline
// degrades that field (0 PRs / nil headline) without dropping the whole tile.
func (h *Handler) buildLiftingSection(ctx context.Context, r *http.Request, userID string, workouts []workout.Workout, unit string, now time.Time, loc *time.Location) *LiftingSection {
	prCount := defer1(ctx, r, "lifting pr count", func() (int, error) {
		ids := thisWeekWorkoutIDs(workouts, now, loc)
		if len(ids) == 0 {
			return 0, nil
		}
		events, err := h.workoutRepo.ListPersonalRecordEventsByWorkouts(ctx, ids)
		if err != nil {
			return 0, err
		}
		return len(events), nil
	})

	headline := defer1(ctx, r, "lifting headline", func() (*Headline1RM, error) {
		prs, err := h.workoutRepo.ListPersonalRecords(ctx, userID)
		if err != nil {
			return nil, err
		}
		return headlineOneRM(ctx, h.workoutRepo, h.exerciseRepo, userID, prs, now), nil
	})

	return buildLifting(workouts, prCount, headline, unit, now, loc)
}

// buildStepsSection assembles the steps tile from the already-fetched entries
// and the user's goal. A goal read error degrades to "no goal" (nil) rather than
// dropping the tile.
func (h *Handler) buildStepsSection(ctx context.Context, r *http.Request, userID string, entries []steps.Entry, now time.Time, loc *time.Location) *StepsSection {
	goal := defer1(ctx, r, "steps goal", func() (steps.Goal, error) {
		return h.stepsRepo.GetGoal(ctx, userID)
	})
	return buildSteps(entries, goal, now, loc)
}

// buildNutritionSection assembles the nutrition tile from today's local-day
// macro aggregate and the user's goals.
func (h *Handler) buildNutritionSection(ctx context.Context, r *http.Request, userID, todayStr string, loc *time.Location) *NutritionSection {
	today := defer1(ctx, r, "nutrition", func() ([]nutrition.DailyMacros, error) {
		start, end, err := daterange.DayBoundsUTC(todayStr, loc)
		if err != nil {
			return nil, err
		}
		return h.nutritionRepo.DailyMacros(ctx, userID, start, end, loc)
	})
	goals := defer1(ctx, r, "nutrition goals", func() (nutrition.MacroGoals, error) {
		return h.nutritionRepo.GetMacroGoals(ctx, userID)
	})
	return buildNutrition(today, goals)
}

// buildBodyweightSection assembles the bodyweight tile from the last 8 weeks of
// measurements and the user's goal.
func (h *Handler) buildBodyweightSection(ctx context.Context, r *http.Request, userID string, since8w time.Time) *BodyweightSection {
	entries := defer1(ctx, r, "bodyweight", func() ([]bodyweight.Entry, error) {
		return h.bodyweightRepo.List(ctx, userID, &since8w, nil)
	})
	goal := defer1(ctx, r, "bodyweight goal", func() (bodyweight.Goal, error) {
		return h.bodyweightRepo.GetBodyweightGoal(ctx, userID)
	})
	return buildBodyweight(entries, goal)
}

// thisWeekWorkoutIDs returns the IDs of workouts performed in the current local
// week — the set whose PR events count toward the tile's "PRs this week".
func thisWeekWorkoutIDs(workouts []workout.Workout, now time.Time, loc *time.Location) []string {
	current := localWeekStart(now, loc)
	var ids []string
	for i := range workouts {
		if localWeekStart(workouts[i].PerformedAt, loc).Equal(current) {
			ids = append(ids, workouts[i].ID)
		}
	}
	return ids
}

// streakDates collects the set of local calendar dates the user was active on,
// across running activities (StartTime), workouts (PerformedAt), and step
// entries with a positive count (their Date is already a local day). Any domain
// that failed to load contributes nothing — the streak degrades gracefully.
func streakDates(runs []activity.Activity, workouts []workout.Workout, stepEntries []steps.Entry, loc *time.Location) map[string]bool {
	active := make(map[string]bool)
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		active[runs[i].StartTime.In(loc).Format("2006-01-02")] = true
	}
	for i := range workouts {
		active[workouts[i].PerformedAt.In(loc).Format("2006-01-02")] = true
	}
	for i := range stepEntries {
		if stepEntries[i].Steps > 0 {
			active[stepEntries[i].Date] = true
		}
	}
	return active
}

// defer1 runs fn and, on error, logs it (tagged with op and the request id) and
// returns the zero value of T. It is the handler's defensive wrapper: every
// section read goes through it so one domain's recoverable failure degrades that
// section to empty/nil instead of failing the whole request.
func defer1[T any](ctx context.Context, r *http.Request, op string, fn func() (T, error)) T {
	v, err := fn()
	if err != nil {
		log.Printf("dashboard: %s for %s: %v", op, requestid.FromContext(r.Context()), err)
		var zero T
		return zero
	}
	return v
}
