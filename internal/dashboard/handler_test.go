package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// --- harness ----------------------------------------------------------

// testNow pins the handler's clock to a fixed Wednesday 13:00 UTC so the
// integration tests' relative seed offsets ("yesterday", "this week") land in a
// deterministic local week regardless of when CI actually runs. Without this the
// week-boundary assertions flake when the suite runs early in a calendar week
// (e.g. Monday 00:xx UTC), where "yesterday" is the prior week and seeds dated
// later in the week fall in the future. It mirrors the fixed reference instant
// the pure-builder unit tests already use.
var testNow = time.Date(2026, 6, 17, 13, 0, 0, 0, time.UTC)

// repos bundles the real SQLite repositories backed by one shared *sql.DB so a
// test can seed across domains and read them all back through the handler.
type repos struct {
	activity   *activity.SQLiteRepository
	workout    *workout.SQLiteRepository
	exercise   *exercise.SQLiteRepository
	steps      *steps.SQLiteRepository
	nutrition  *nutrition.SQLiteRepository
	bodyweight *bodyweight.SQLiteRepository
	user       *user.SQLiteRepository
}

// newTestEnv builds the shared-DB repositories, syncs the exercise catalog (so
// workouts reference valid exercise IDs and headline name lookups resolve),
// seeds a user row, and returns a mounted router plus the seeded user ID.
func newTestEnv(t *testing.T) (*chi.Mux, *repos, string) {
	t.Helper()
	db := dbtest.New(t)
	rp := &repos{
		activity:   activity.NewSQLiteRepository(db, activity.NewMemoryArchiver()),
		workout:    workout.NewSQLiteRepository(db),
		exercise:   exercise.NewSQLiteRepository(db),
		steps:      steps.NewSQLiteRepository(db),
		nutrition:  nutrition.NewSQLiteRepository(db),
		bodyweight: bodyweight.NewSQLiteRepository(db),
		user:       user.NewSQLiteRepository(db),
	}
	if err := rp.exercise.SyncCatalog(context.Background(), exercise.Catalog); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}

	u := &user.User{
		Email:        "dash@example.com",
		DisplayName:  "Dash User",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
		Timezone:     "UTC",
	}
	if err := rp.user.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := chi.NewRouter()
	h := NewHandler(rp.activity, rp.workout, rp.exercise, rp.steps, rp.nutrition, rp.bodyweight, rp.user)
	h.now = func() time.Time { return testNow }
	h.Mount(r)
	return r, rp, u.ID
}

// get drives GET /dashboard/summary for userID with the given query string
// (e.g. "?timezone=UTC") and returns the recorder.
func get(t *testing.T, r *chi.Mux, userID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/summary"+query, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// summaryEnvelope mirrors the {message,data} envelope wrapping a Summary. The
// Summary type itself is reused directly since the test lives in the package.
type summaryEnvelope struct {
	Message string  `json:"message"`
	Data    Summary `json:"data"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) Summary {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env summaryEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

// --- seeding helpers --------------------------------------------------

func seedRun(t *testing.T, rp *repos, userID string, start time.Time, distanceMeters float64) {
	t.Helper()
	a := &activity.Activity{
		UserID:           userID,
		ActivityType:     activity.ActivityRunning,
		IngestSource:     activity.IngestManualTCX,
		SourceActivityID: start.Format("20060102T150405"),
		StartTime:        start,
		DistanceMeters:   distanceMeters,
		DurationSeconds:  1800,
	}
	if err := rp.activity.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func seedWorkout(t *testing.T, rp *repos, userID string, performedAt time.Time, exerciseID string, weight float64, reps int) {
	t.Helper()
	ended := performedAt.Add(45 * time.Minute)
	w := &workout.Workout{
		UserID:      userID,
		Name:        "Session",
		PerformedAt: performedAt,
		EndedAt:     &ended,
		Exercises: []workout.WorkoutExercise{
			{
				ExerciseID: exerciseID,
				Order:      0,
				Sets: []workout.Set{
					{Reps: reps, Weight: weight, Unit: user.WeightUnitPounds},
				},
			},
		},
	}
	if err := rp.workout.Create(context.Background(), w); err != nil {
		t.Fatalf("seed workout: %v", err)
	}
}

func seedSteps(t *testing.T, rp *repos, userID, date string, count int) {
	t.Helper()
	if _, err := rp.steps.UpsertEntry(context.Background(), &steps.Entry{UserID: userID, Date: date, Steps: count}); err != nil {
		t.Fatalf("seed steps: %v", err)
	}
}

func seedNutrition(t *testing.T, rp *repos, userID string, consumedAt time.Time) {
	t.Helper()
	name := "Oatmeal"
	e := &nutrition.NutritionLogEntry{
		UserID:         userID,
		ConsumedAt:     consumedAt,
		CustomMealName: &name,
		Quantity:       1,
		Calories:       400,
		ProteinG:       20,
		FatG:           10,
		CarbsG:         50,
		Meal:           nutrition.MealBreakfast,
	}
	if err := rp.nutrition.CreateNutritionLogEntry(context.Background(), e); err != nil {
		t.Fatalf("seed nutrition: %v", err)
	}
}

func seedBodyweight(t *testing.T, rp *repos, userID string, measuredAt time.Time, weight float64) {
	t.Helper()
	e := &bodyweight.Entry{UserID: userID, Weight: weight, Unit: user.WeightUnitPounds, MeasuredAt: measuredAt}
	if err := rp.bodyweight.Create(context.Background(), e); err != nil {
		t.Fatalf("seed bodyweight: %v", err)
	}
}

// --- tests ------------------------------------------------------------

func TestSummary_Full(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	now := testNow
	thisWeek := now.Add(-24 * time.Hour)
	lastWeek := now.AddDate(0, 0, -8)
	todayStr := now.Format("2006-01-02")

	// Runs across two local weeks → spark + streak across weeks.
	seedRun(t, rp, userID, thisWeek, 5000)
	seedRun(t, rp, userID, lastWeek, 8000)

	// Workouts this week + last week; sets populate 1RM history → headline.
	seedWorkout(t, rp, userID, thisWeek, "barbell-bench-press", 185, 5)
	seedWorkout(t, rp, userID, lastWeek, "barbell-high-bar-back-squat", 225, 5)

	// Steps for several days incl. today.
	seedSteps(t, rp, userID, todayStr, 9000)
	seedSteps(t, rp, userID, now.AddDate(0, 0, -1).Format("2006-01-02"), 7000)
	seedSteps(t, rp, userID, now.AddDate(0, 0, -2).Format("2006-01-02"), 11000)
	if _, err := rp.steps.UpsertGoal(context.Background(), steps.Goal{UserID: userID, Goal: 10000}, now); err != nil {
		t.Fatalf("steps goal: %v", err)
	}

	// Today's nutrition + macro goals.
	seedNutrition(t, rp, userID, now)
	if _, err := rp.nutrition.UpsertMacroGoals(context.Background(), nutrition.MacroGoals{UserID: userID, Calories: 2200, ProteinG: 160, CarbsG: 220, FatG: 70}, now); err != nil {
		t.Fatalf("macro goals: %v", err)
	}

	// Bodyweight entries + goal.
	seedBodyweight(t, rp, userID, now.AddDate(0, 0, -10), 182)
	seedBodyweight(t, rp, userID, now.AddDate(0, 0, -3), 180)
	if _, err := rp.bodyweight.UpsertBodyweightGoal(context.Background(), bodyweight.Goal{UserID: userID, Weight: 175, Unit: user.WeightUnitPounds}, now); err != nil {
		t.Fatalf("bodyweight goal: %v", err)
	}

	s := decode(t, get(t, r, userID, "?timezone=UTC"))

	if s.Running == nil {
		t.Fatal("running section nil, want present")
	}
	if s.Running.LatestRun == nil || s.Running.LatestRun.DistanceMeters != 5000 {
		t.Errorf("latest run = %+v, want 5000m", s.Running.LatestRun)
	}
	if len(s.Running.WeeklyDistanceSpark) != sparkWeeks {
		t.Errorf("running spark len = %d, want %d", len(s.Running.WeeklyDistanceSpark), sparkWeeks)
	}

	if s.Lifting == nil {
		t.Fatal("lifting section nil, want present")
	}
	if s.Lifting.Unit != "lb" {
		t.Errorf("lifting unit = %q, want lb", s.Lifting.Unit)
	}
	if s.Lifting.CurrentWeek.Sessions != 1 {
		t.Errorf("lifting sessions this week = %d, want 1", s.Lifting.CurrentWeek.Sessions)
	}
	if s.Lifting.HeadlineEstimated1RM == nil || s.Lifting.HeadlineEstimated1RM.Value <= 0 {
		t.Errorf("headline 1RM = %+v, want positive value", s.Lifting.HeadlineEstimated1RM)
	}
	if s.Lifting.CurrentWeek.PRs < 0 {
		t.Errorf("prs = %d, want >= 0", s.Lifting.CurrentWeek.PRs)
	}

	if s.Steps == nil {
		t.Fatal("steps section nil, want present")
	}
	if s.Steps.Today != 9000 {
		t.Errorf("steps today = %d, want 9000", s.Steps.Today)
	}
	if s.Steps.Goal == nil || *s.Steps.Goal != 10000 {
		t.Errorf("steps goal = %v, want 10000", s.Steps.Goal)
	}

	if s.Nutrition == nil {
		t.Fatal("nutrition section nil, want present")
	}
	if s.Nutrition.Today.Calories != 400 {
		t.Errorf("nutrition calories = %v, want 400", s.Nutrition.Today.Calories)
	}
	if s.Nutrition.Goals == nil || s.Nutrition.Goals.Calories != 2200 {
		t.Errorf("nutrition goals = %v, want 2200 cal", s.Nutrition.Goals)
	}

	if s.Bodyweight == nil {
		t.Fatal("bodyweight section nil, want present")
	}
	if s.Bodyweight.Current != 180 {
		t.Errorf("bodyweight current = %v, want 180", s.Bodyweight.Current)
	}
	if s.Bodyweight.Goal == nil || s.Bodyweight.Goal.Weight != 175 {
		t.Errorf("bodyweight goal = %v, want 175", s.Bodyweight.Goal)
	}

	if s.Streak.Weeks < 1 {
		t.Errorf("streak weeks = %d, want >= 1", s.Streak.Weeks)
	}
}

func TestSummary_Partial(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	now := testNow
	todayStr := now.Format("2006-01-02")

	seedWorkout(t, rp, userID, now.Add(-2*time.Hour), "barbell-bench-press", 185, 5)
	seedSteps(t, rp, userID, todayStr, 8000)

	s := decode(t, get(t, r, userID, "?timezone=UTC"))

	if s.Running != nil {
		t.Errorf("running = %+v, want nil", s.Running)
	}
	if s.Nutrition != nil {
		t.Errorf("nutrition = %+v, want nil", s.Nutrition)
	}
	if s.Bodyweight != nil {
		t.Errorf("bodyweight = %+v, want nil", s.Bodyweight)
	}
	if s.Lifting == nil {
		t.Error("lifting nil, want present")
	}
	if s.Steps == nil {
		t.Error("steps nil, want present")
	}
	if s.Streak.ActiveDaysThisWeek < 1 {
		t.Errorf("active days this week = %d, want >= 1", s.Streak.ActiveDaysThisWeek)
	}
}

func TestSummary_BrandNew(t *testing.T) {
	r, _, userID := newTestEnv(t)

	s := decode(t, get(t, r, userID, "?timezone=UTC"))

	if s.Running != nil || s.Lifting != nil || s.Steps != nil || s.Nutrition != nil || s.Bodyweight != nil {
		t.Errorf("want all sections nil, got %+v", s)
	}
	if s.Streak.Weeks != 0 || s.Streak.ActiveDaysThisWeek != 0 {
		t.Errorf("streak = %+v, want zeroed", s.Streak)
	}
	for i, active := range s.Streak.Week {
		if active {
			t.Errorf("streak week[%d] active, want all false", i)
		}
	}
}

// errBodyweightRepo embeds a real bodyweight repository but forces List to
// fail, simulating a recoverable read error in exactly one domain. Every other
// method delegates to the embedded real repo, so it satisfies the full
// bodyweight.Repository interface with a single overridden method.
type errBodyweightRepo struct {
	bodyweight.Repository
}

func (errBodyweightRepo) List(ctx context.Context, userID string, since, until *time.Time) ([]bodyweight.Entry, error) {
	return nil, errors.New("bodyweight list boom")
}

// TestSummary_DomainReadError_DegradesToNilSection pins the handler's central
// resilience guarantee: a recoverable error in one domain's read yields a nil
// section while the request still returns 200 and the other sections + streak
// stay present. Here the bodyweight List read fails (via a fake repo) while the
// other repos are real and seeded with steps + a workout.
func TestSummary_DomainReadError_DegradesToNilSection(t *testing.T) {
	db := dbtest.New(t)
	rp := &repos{
		activity:   activity.NewSQLiteRepository(db, activity.NewMemoryArchiver()),
		workout:    workout.NewSQLiteRepository(db),
		exercise:   exercise.NewSQLiteRepository(db),
		steps:      steps.NewSQLiteRepository(db),
		nutrition:  nutrition.NewSQLiteRepository(db),
		bodyweight: bodyweight.NewSQLiteRepository(db),
		user:       user.NewSQLiteRepository(db),
	}
	if err := rp.exercise.SyncCatalog(context.Background(), exercise.Catalog); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	u := &user.User{
		Email:        "dash-err@example.com",
		DisplayName:  "Dash Err",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
		Timezone:     "UTC",
	}
	if err := rp.user.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := u.ID

	now := testNow
	todayStr := now.Format("2006-01-02")
	// Seed at least one other domain so its section proves the request didn't
	// abort early. Bodyweight is left seeded too — its read still fails, so the
	// section must be nil regardless of underlying data.
	seedSteps(t, rp, userID, todayStr, 8000)
	seedWorkout(t, rp, userID, now.Add(-2*time.Hour), "barbell-bench-press", 185, 5)
	seedBodyweight(t, rp, userID, now.AddDate(0, 0, -3), 180)

	// Wire the handler with the failing bodyweight repo; all others are real.
	r := chi.NewRouter()
	h := NewHandler(rp.activity, rp.workout, rp.exercise, rp.steps, rp.nutrition, errBodyweightRepo{rp.bodyweight}, rp.user)
	h.now = func() time.Time { return testNow }
	h.Mount(r)

	rec := get(t, r, userID, "?timezone=UTC")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	s := decode(t, rec)

	if s.Bodyweight != nil {
		t.Errorf("bodyweight section = %+v, want nil (its read errored)", s.Bodyweight)
	}
	if s.Steps == nil {
		t.Error("steps section nil, want present (request must not abort early)")
	}
	if s.Lifting == nil {
		t.Error("lifting section nil, want present (request must not abort early)")
	}
	if s.Streak.ActiveDaysThisWeek < 1 {
		t.Errorf("streak active days this week = %d, want >= 1 (streak must still be derived)", s.Streak.ActiveDaysThisWeek)
	}
}

func TestSummary_Streak(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	loc := time.UTC
	now := testNow.In(loc)
	monday := localWeekStart(now, loc)

	// Current week: a run on Monday, plus steps that met the user's goal on
	// Wednesday. Prior week: a completed workout so the streak spans 2 weeks.
	// Passive steps only count once a goal is set and met.
	seedRun(t, rp, userID, monday.Add(10*time.Hour), 5000)
	if _, err := rp.steps.UpsertGoal(context.Background(), steps.Goal{UserID: userID, Goal: 8000}, now); err != nil {
		t.Fatalf("steps goal: %v", err)
	}
	seedSteps(t, rp, userID, monday.AddDate(0, 0, 2).Format("2006-01-02"), 9000) // >= goal → counts
	seedWorkout(t, rp, userID, monday.AddDate(0, 0, -5), "barbell-high-bar-back-squat", 225, 5)

	s := decode(t, get(t, r, userID, "?timezone=UTC"))

	if s.Streak.Weeks < 2 {
		t.Errorf("streak weeks = %d, want >= 2", s.Streak.Weeks)
	}
	if s.Streak.ActiveDaysThisWeek != 2 {
		t.Errorf("active days this week = %d, want 2", s.Streak.ActiveDaysThisWeek)
	}
	if !s.Streak.Week[0] {
		t.Error("Monday not active, want active (run)")
	}
	if !s.Streak.Week[2] {
		t.Error("Wednesday not active, want active (steps met goal)")
	}
	if s.Streak.Week[1] {
		t.Error("Tuesday active, want inactive")
	}
}

// TestSummary_StreakOnlyCountsCompletedActivity exercises the streak's
// completion rules end-to-end through the handler: passive steps only count
// when the user's goal is met, and a started-but-abandoned workout (no end
// time, no sets) does not count. Each case seeds on Monday of the fixed test
// week so the Week[0] assertion is unambiguous.
func TestSummary_StreakOnlyCountsCompletedActivity(t *testing.T) {
	loc := time.UTC
	now := testNow.In(loc)
	monday := localWeekStart(now, loc)
	mondayStr := monday.Format("2006-01-02")

	t.Run("steps without a goal do not count", func(t *testing.T) {
		r, rp, userID := newTestEnv(t)
		seedSteps(t, rp, userID, mondayStr, 50000) // high, but no goal set
		s := decode(t, get(t, r, userID, "?timezone=UTC"))
		if s.Streak.Week[0] {
			t.Error("steps with no goal lit Monday; want inactive")
		}
	})

	t.Run("steps meeting the goal count", func(t *testing.T) {
		r, rp, userID := newTestEnv(t)
		if _, err := rp.steps.UpsertGoal(context.Background(), steps.Goal{UserID: userID, Goal: 10000}, now); err != nil {
			t.Fatalf("steps goal: %v", err)
		}
		seedSteps(t, rp, userID, mondayStr, 12000)
		s := decode(t, get(t, r, userID, "?timezone=UTC"))
		if !s.Streak.Week[0] {
			t.Error("steps meeting goal didn't light Monday; want active")
		}
	})

	t.Run("abandoned workout does not count", func(t *testing.T) {
		r, rp, userID := newTestEnv(t)
		// Started but never finished: no end time, no sets.
		w := &workout.Workout{UserID: userID, PerformedAt: monday.Add(10 * time.Hour)}
		if err := rp.workout.Create(context.Background(), w); err != nil {
			t.Fatalf("seed abandoned workout: %v", err)
		}
		s := decode(t, get(t, r, userID, "?timezone=UTC"))
		if s.Streak.Week[0] {
			t.Error("abandoned workout lit Monday; want inactive")
		}
	})
}

func TestSummary_Timezone(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("load denver: %v", err)
	}

	// Seed a run at 05:00 UTC on "today" (UTC). 05:00 UTC is the prior calendar
	// day at 22:00 in America/Denver (UTC-7/-6), so the active local day — and
	// thus which weekday flag lights up in the streak — depends on the
	// requested timezone. This pins down that bucketing happens in `loc`, not
	// in UTC.
	now := testNow
	boundaryUTC := time.Date(now.Year(), now.Month(), now.Day(), 5, 0, 0, 0, time.UTC)
	seedRun(t, rp, userID, boundaryUTC, 4000)

	utcDay := boundaryUTC.Format("2006-01-02")
	denverDay := boundaryUTC.In(denver).Format("2006-01-02")
	if utcDay == denverDay {
		t.Fatalf("boundary premise broken: %s == %s (expected different local days)", utcDay, denverDay)
	}

	sUTC := decode(t, get(t, r, userID, "?timezone=UTC"))
	sDenver := decode(t, get(t, r, userID, "?timezone=America/Denver"))

	// The active weekday index within the current week must differ between the
	// two timezones because the run maps to a different local calendar day.
	utcIdx := weekdayIndex(boundaryUTC, time.UTC)
	denverIdx := weekdayIndex(boundaryUTC, denver)
	if utcIdx >= 0 && !sUTC.Streak.Week[utcIdx] {
		t.Errorf("UTC streak week[%d] not active for the run's UTC local day", utcIdx)
	}
	if denverIdx >= 0 && !sDenver.Streak.Week[denverIdx] {
		t.Errorf("Denver streak week[%d] not active for the run's Denver local day", denverIdx)
	}
	if utcIdx >= 0 && denverIdx >= 0 && utcIdx != denverIdx && sUTC.Streak.Week[denverIdx] {
		t.Errorf("UTC streak lit the Denver-local weekday[%d]; bucketing leaked across timezones", denverIdx)
	}
}

// weekdayIndex returns the Mon=0..Sun=6 index of t's local day within its own
// week in loc, mirroring localWeekStart's offset math.
func weekdayIndex(t time.Time, loc *time.Location) int {
	return (int(t.In(loc).Weekday()) + 6) % 7
}

func TestSummary_MissingTimezone(t *testing.T) {
	r, _, userID := newTestEnv(t)
	rec := get(t, r, userID, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSummary_InvalidTimezone(t *testing.T) {
	r, _, userID := newTestEnv(t)
	rec := get(t, r, userID, "?timezone=Not/AZone")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
