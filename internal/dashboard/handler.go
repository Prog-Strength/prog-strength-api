package dashboard

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/activity/strength"
	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/bloodpressure"
	"github.com/Prog-Strength/prog-strength-api/internal/bodyweight"
	"github.com/Prog-Strength/prog-strength-api/internal/daterange"
	"github.com/Prog-Strength/prog-strength-api/internal/exercise"
	"github.com/Prog-Strength/prog-strength-api/internal/hrzones"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/nutrition"
	"github.com/Prog-Strength/prog-strength-api/internal/recoverytrend"
	"github.com/Prog-Strength/prog-strength-api/internal/requestid"
	"github.com/Prog-Strength/prog-strength-api/internal/steps"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
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
	activityRepo      activity.Repository
	workoutRepo       strength.Repository
	exerciseRepo      exercise.Repository
	stepsRepo         steps.Repository
	nutritionRepo     nutrition.Repository
	bodyweightRepo    bodyweight.Repository
	bloodPressureRepo bloodpressure.Repository
	userRepo          user.Repository
	whoopConns        whoopconn.Repository
	whoopRecovery     whooprecovery.Repository
	layoutRepo        Repository
	quoteRerollRepo   QuoteRerollRepository

	// recovery derives the baseline/HRV blocks of the recovery tile from the
	// fetched window. Mandatory: a handler without it would serve a half-built
	// recovery payload, so it's a constructor arg rather than a setter.
	recovery *recoverytrend.Engine

	// hrEngine classifies each week run's average HR into a zone for the
	// Run Effort tile. Optional: injected post-construction via
	// SetHRZonesEngine (mirroring activity.Handler); nil leaves every
	// HeartRateZone nil.
	hrEngine *hrzones.Engine
	// hrWindow is the recency window RecentHRStats summarizes for the
	// reference max-HR estimate.
	hrWindow time.Duration

	// now sources the current instant for all local-week/local-day bucketing.
	// It defaults to time.Now; tests override it to pin a fixed reference time so
	// week-boundary assertions don't flake on the real calendar.
	now func() time.Time
}

// NewHandler builds a dashboard Handler backed by the given read repositories.
func NewHandler(
	activityRepo activity.Repository,
	workoutRepo strength.Repository,
	exerciseRepo exercise.Repository,
	stepsRepo steps.Repository,
	nutritionRepo nutrition.Repository,
	bodyweightRepo bodyweight.Repository,
	bloodPressureRepo bloodpressure.Repository,
	userRepo user.Repository,
	whoopConns whoopconn.Repository,
	whoopRecovery whooprecovery.Repository,
	layoutRepo Repository,
	quoteRerollRepo QuoteRerollRepository,
	recoveryEngine *recoverytrend.Engine,
) *Handler {
	return &Handler{
		activityRepo:      activityRepo,
		workoutRepo:       workoutRepo,
		exerciseRepo:      exerciseRepo,
		stepsRepo:         stepsRepo,
		nutritionRepo:     nutritionRepo,
		bodyweightRepo:    bodyweightRepo,
		bloodPressureRepo: bloodPressureRepo,
		userRepo:          userRepo,
		whoopConns:        whoopConns,
		whoopRecovery:     whoopRecovery,
		layoutRepo:        layoutRepo,
		quoteRerollRepo:   quoteRerollRepo,
		recovery:          recoveryEngine,
		now:               time.Now,
	}
}

// SetHRZonesEngine wires the heart-rate-zone engine (and its recency window)
// in so week runs carry a heart_rate_zone when the Run Effort tile is
// enabled. Called from server wiring after construction, mirroring
// activity.Handler's setter; a setter rather than a constructor arg keeps
// NewHandler's signature and every existing test untouched. Safe to never
// call — buildRunningSection nil-guards and leaves zones nil.
func (h *Handler) SetHRZonesEngine(e *hrzones.Engine, window time.Duration) {
	h.hrEngine = e
	h.hrWindow = window
}

// Mount registers the dashboard route. Callers are expected to have already
// wrapped the router in auth.RequireUser — summary reads the user ID from
// context and assumes it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/dashboard/summary", h.summary)
	r.Post("/dashboard/quote/reroll", h.rerollQuote)
	r.Put("/dashboard/layout", h.putLayout)
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

	// One unified read over the base table covers every session type. It
	// replaces the pre-042 pair (an endurance-only activity read plus a
	// separate capped workout read): all-types == old endurance ∪ old lifts,
	// preserving that equivalence. endurance (non-strength rows) feeds the
	// running tile and the streak's cardio path exactly as the old
	// ExcludeStrength read did; the strength rows are re-hydrated into
	// workouts below for the lifting tile and the streak's completion path.
	// Reading the base directly also drops the old workout read's 50-row cap,
	// which silently truncated a heavy lifter's year-long streak window (the
	// sparkLookbackWeeks comment above always intended a full-year pull).
	sessions := defer1(ctx, r, "activities", func() ([]activity.Activity, error) {
		return h.activityRepo.ListInRange(ctx, userID, &since53w, nil, activity.TypeFilter{})
	})
	endurance := enduranceOnly(sessions)
	stepEntries := defer1(ctx, r, "steps", func() ([]steps.Entry, error) {
		entries, _, err := h.stepsRepo.List(ctx, userID, &since53wStr, &todayStr, 0, nil)
		return entries, err
	})
	// The streak only credits passive steps on days that met the user's goal.
	// A failed goal read degrades to "steps don't count" (zero) rather than
	// failing the request, consistent with the other defensive section reads.
	stepGoal := defer1(ctx, r, "steps goal", func() (steps.Goal, error) {
		return h.stepsRepo.GetGoal(ctx, userID)
	})

	// The layout decides which tiles this user sees; only enabled tiles are
	// computed. A layout-read failure degrades to the default rather than 500
	// (see resolveLayout) — the same resilience principle as the per-section
	// defer1 reads. The two shared reads above (the unified activity list and
	// steps entries + goal) stay UNGATED: the streak walks over them regardless
	// of whether the Steps/Running tiles are enabled, so disabling a tile must
	// not silently change the streak.
	// Section structure never reaches the builders: which tiles to compute is
	// the flattened union across sections, so a tile behaves identically in the
	// first section and the fifth.
	sections := h.resolveLayout(ctx, r, userID)
	layout := Layout{Sections: sections}.TileIDs()
	enabled := make(map[TileID]bool, len(layout))
	for _, id := range layout {
		enabled[id] = true
	}

	// Strength hydration is a second read; only run it when a tile actually
	// consumes workouts — the lifting tile, or the streak's completion path.
	var workouts []strength.Workout
	if enabled[TileLifting] || enabled[TileStreak] {
		workouts = defer1(ctx, r, "workout exercises", func() ([]strength.Workout, error) {
			return h.hydrateStrengthWorkouts(ctx, sessions)
		})
	}

	// Built as a map so a tile enabled-but-with-no-data serializes as JSON null
	// (a nil typed pointer boxed in any marshals to null) while a tile absent
	// from the layout is absent from the response — a distinction an omitempty
	// struct cannot express.
	out := map[string]any{"sections": sections}
	// Every running-family tile reads the ONE shared "running" section, so it
	// is built once when ANY family tile is enabled and emitted only under the
	// "running" key — the same section-key/layout divergence the recovery
	// family established below.
	runningFamily := []TileID{
		TileRunning, TileRunningLog, TileRunningEffort, TileRunningVertical,
	}
	for _, id := range runningFamily {
		if enabled[id] {
			out[string(TileRunning)] = h.buildRunningSection(
				ctx, r, userID, endurance, now, loc, enabled[TileRunningEffort],
			)
			break
		}
	}
	if enabled[TileWalking] {
		out[string(TileWalking)] = buildWalking(endurance, now, loc)
	}
	if enabled[TileCycling] {
		out[string(TileCycling)] = buildCycling(endurance, now, loc)
	}
	if enabled[TileHiking] {
		out[string(TileHiking)] = buildHiking(endurance, now, loc)
	}
	if enabled[TileLifting] {
		out[string(TileLifting)] = h.buildLiftingSection(ctx, r, userID, workouts, unit, now, loc)
	}
	if enabled[TileSteps] {
		out[string(TileSteps)] = h.buildStepsSection(ctx, r, userID, stepEntries, now, loc)
	}
	if enabled[TileNutrition] {
		out[string(TileNutrition)] = h.buildNutritionSection(ctx, r, userID, todayStr, loc)
	}
	if enabled[TileBodyweight] {
		out[string(TileBodyweight)] = h.buildBodyweightSection(ctx, r, userID, since8w)
	}
	if enabled[TileBloodPressure] {
		out[string(TileBloodPressure)] = h.buildBloodPressureSection(ctx, r, userID, since8w, now, loc)
	}
	// Every recovery-family tile reads the ONE shared "recovery" section, so it
	// is built once when ANY family tile is enabled and emitted only under the
	// "recovery" key. This is the first place the response's section-key set
	// deliberately diverges from layout: a layout of [hrv_balance] yields a
	// "recovery" key and no "hrv_balance" key (pinned by the summary layout
	// tests). The web adapter already reads it that way.
	recoveryFamily := []TileID{
		TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryTrend, TileRecoveryLog,
	}
	for _, id := range recoveryFamily {
		if enabled[id] {
			out[string(TileRecovery)] = h.buildRecoverySection(ctx, r, userID, now, loc)
			break
		}
	}
	if enabled[TileStreak] {
		out[string(TileStreak)] = buildStreak(streakDates(endurance, workouts, stepEntries, stepGoal.Goal, loc), now, loc)
	}
	// The quote itself needs no repo read — the corpus is embedded — but the
	// user's reroll position does, so the tile can come back to the quote they
	// last advanced to instead of snapping to the day's. resolveQuoteOffset
	// absorbs a failed read as offset 0, so this stays a section that cannot
	// fail the request.
	if enabled[TileQuote] {
		out[string(TileQuote)] = buildQuote(userID, todayStr, h.resolveQuoteOffset(ctx, r, userID, todayStr))
	}
	// TileWeather deliberately has no builder and no section key here: the tile
	// self-fetches from GET /weather, so a slow OpenWeather day can never slow
	// the dashboard (SOW: "Why weather is not a summary section"). A layout
	// containing it validates and renders; the summary just never mentions it.

	httpresp.OK(w, "dashboard summary", out)
}

// buildRunningSection fetches the running metrics and assembles the section
// from them plus the already-fetched 53-week run list. withZones additionally
// classifies each HR-bearing week run against the user's max-HR reference —
// only when the Run Effort tile is enabled, because the reference is the
// family's one extra read. A nil engine or a failed reference read leaves
// every zone nil (degraded, never a 500, never a fabricated zone): the defer1
// wrapper yields a nil *Reference on error, which skips classification.
func (h *Handler) buildRunningSection(ctx context.Context, r *http.Request, userID string, runs []activity.Activity, now time.Time, loc *time.Location, withZones bool) *RunningSection {
	metrics := defer1(ctx, r, "running metrics", func() (activity.Metrics, error) {
		return h.activityRepo.RunningMetrics(ctx, userID, now, loc)
	})
	section := buildRunning(metrics, runs, now, loc)
	if section == nil || !withZones || h.hrEngine == nil {
		return section
	}

	ref := defer1(ctx, r, "running hr reference", func() (*hrzones.Reference, error) {
		stats, err := h.activityRepo.RecentHRStats(ctx, userID, h.hrWindow, "")
		if err != nil {
			return nil, err
		}
		estimated := h.hrEngine.EstimateReference(stats)
		return &estimated, nil
	})
	if ref == nil {
		return section
	}
	for i := range section.WeekRuns {
		if bpm := section.WeekRuns[i].AvgHeartRateBpm; bpm != nil {
			// The engine is 0-indexed; the wire field is 1-indexed to match
			// the web's --zone-1..5 tokens.
			zone := h.hrEngine.ZoneForBPM(*ref, *bpm) + 1
			section.WeekRuns[i].HeartRateZone = &zone
		}
	}
	return section
}

// buildLiftingSection assembles the lifting tile: weekly volume + duration from
// the 53-week workout list, this-week PR count from the PR-event table, and the
// headline 1RM from the user's PRs. A failure computing the PR count or headline
// degrades that field (0 PRs / nil headline) without dropping the whole tile.
func (h *Handler) buildLiftingSection(ctx context.Context, r *http.Request, userID string, workouts []strength.Workout, unit string, now time.Time, loc *time.Location) *LiftingSection {
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

// buildBloodPressureSection assembles the blood-pressure tile from the trailing
// window of readings (since covers 30 days of average plus the daily sparks).
func (h *Handler) buildBloodPressureSection(ctx context.Context, r *http.Request, userID string, since time.Time, now time.Time, loc *time.Location) *BloodPressureSection {
	entries := defer1(ctx, r, "blood pressure", func() ([]bloodpressure.Entry, error) {
		return h.bloodPressureRepo.List(ctx, userID, &since, nil)
	})
	return buildBloodPressure(entries, now, loc)
}

// buildRecoverySection assembles the Whoop recovery tile. It is present only
// when the user has a CONNECTED Whoop connection: an absent/revoked/errored
// connection (or a failed connection read) yields a nil section so the card
// stays hidden. When connected, it fetches 2×baseline_window_days+1 local days
// of recovery — a window of lead-in ahead of the charted window, so every
// charted day has a full trailing baseline — and builds today's row, the 7-day
// resting-HR sparkline, the date-aligned days history, and the derived
// baseline/HRV blocks.
// A failed recovery read degrades to nil (never a 500), like the other section
// reads.
func (h *Handler) buildRecoverySection(ctx context.Context, r *http.Request, userID string, now time.Time, loc *time.Location) *RecoverySection {
	conn, err := h.whoopConns.Get(ctx, userID)
	if err != nil {
		if !errors.Is(err, whoopconn.ErrNotFound) {
			log.Printf("dashboard: whoop connection for %s: %v", requestid.FromContext(r.Context()), err)
		}
		return nil
	}
	if conn.Status != whoopconn.StatusConnected {
		return nil
	}

	// TWO baseline windows plus today: baseline_window_days of lead-in so the
	// OLDEST charted day still has a full trailing sample, then the charted
	// window itself (61 dates at the defaults). Same single indexed ListRange
	// call as before — idx_user_whoop_recovery_user_date covers (user_id,
	// date DESC) — the read just returns ≤61 rows instead of ≤31.
	win := h.recovery.BaselineWindowDays()
	sinceStr := now.In(loc).AddDate(0, 0, -2*win).Format("2006-01-02")
	untilStr := now.In(loc).Format("2006-01-02")
	entries := defer1(ctx, r, "whoop recovery", func() ([]whooprecovery.Entry, error) {
		return h.whoopRecovery.ListRange(ctx, userID, sinceStr, untilStr)
	})
	return buildWhoop(entries, h.recovery, now, loc)
}

// thisWeekWorkoutIDs returns the IDs of workouts performed in the current local
// week — the set whose PR events count toward the tile's "PRs this week".
func thisWeekWorkoutIDs(workouts []strength.Workout, now time.Time, loc *time.Location) []string {
	current := localWeekStart(now, loc)
	var ids []string
	for i := range workouts {
		if localWeekStart(workouts[i].PerformedAt, loc).Equal(current) {
			ids = append(ids, workouts[i].ID)
		}
	}
	return ids
}

// enduranceOnly returns the non-strength_training rows of the unified session
// list — the endurance sessions the running tile and the streak's cardio path
// consume, matching the pre-042 ExcludeStrength read exactly (the running
// builders further narrow to running; walks/rides pass through as before).
func enduranceOnly(sessions []activity.Activity) []activity.Activity {
	out := make([]activity.Activity, 0, len(sessions))
	for i := range sessions {
		if sessions[i].ActivityType != activity.ActivityStrengthTraining {
			out = append(out, sessions[i])
		}
	}
	return out
}

// hydrateStrengthWorkouts reconstructs the strength.Workout values the lifting
// tile and streak completion check need from the unified list's strength rows.
// The base row supplies id, start (performed_at), and duration; the exercises
// come from one batched detail read on the workout repo. EndedAt derives from
// duration > 0 (start+duration), else nil. Known edge: the base Activity model
// loads duration_seconds through COALESCE(…, 0), so NULL (an in-progress,
// end-less lift) and an explicit 0 are indistinguishable here — a completed
// 0-second workout would derive as in-progress. Accepted rather than
// re-plumbing a nullable duration: a real finished workout always has a
// positive duration, so the case is practically unreachable.
func (h *Handler) hydrateStrengthWorkouts(ctx context.Context, sessions []activity.Activity) ([]strength.Workout, error) {
	var ids []string
	for i := range sessions {
		if sessions[i].ActivityType == activity.ActivityStrengthTraining {
			ids = append(ids, sessions[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	exByID, err := h.workoutRepo.ListExercisesByWorkoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]strength.Workout, 0, len(ids))
	for i := range sessions {
		a := sessions[i]
		if a.ActivityType != activity.ActivityStrengthTraining {
			continue
		}
		w := strength.Workout{
			ID:          a.ID,
			UserID:      a.UserID,
			PerformedAt: a.StartTime,
			Exercises:   exByID[a.ID],
		}
		if a.DurationSeconds > 0 {
			end := a.StartTime.Add(time.Duration(a.DurationSeconds) * time.Second)
			w.EndedAt = &end
		}
		out = append(out, w)
	}
	return out, nil
}

// streakDates collects the set of local calendar dates the user genuinely
// completed an activity on. A day counts when ANY of these hold:
//
//   - a completed cardio activity session (running/walking/cycling/other) —
//     activity rows only exist once fully ingested, so they are inherently
//     complete; strength_training rows are skipped because they are HR
//     enrichment attached to a workout, counted via the workout path below.
//   - a completed workout — finished (EndedAt set) AND with at least one logged
//     set. An empty or abandoned session (a row created when the user merely
//     started one) does not count.
//   - a day whose passive step total met the user's step goal. stepGoal is the
//     user's daily target; when it is 0 (never set) passive steps never count —
//     there is no magic-number fallback.
//
// Any domain that failed to load contributes nothing — the streak degrades
// gracefully.
func streakDates(runs []activity.Activity, workouts []strength.Workout, stepEntries []steps.Entry, stepGoal int, loc *time.Location) map[string]bool {
	active := make(map[string]bool)
	for i := range runs {
		// The summary handler already passes enduranceOnly output, so this
		// guard is redundant on that path — it stays because streakDates'
		// own contract (unit-tested directly with mixed input) is that a
		// strength row never lights a day here; only the completion-checked
		// workout path below may.
		if runs[i].ActivityType == activity.ActivityStrengthTraining {
			continue
		}
		active[runs[i].StartTime.In(loc).Format("2006-01-02")] = true
	}
	for i := range workouts {
		if !workoutCompleted(workouts[i]) {
			continue
		}
		active[workouts[i].PerformedAt.In(loc).Format("2006-01-02")] = true
	}
	if stepGoal > 0 {
		for i := range stepEntries {
			if stepEntries[i].Steps >= stepGoal {
				active[stepEntries[i].Date] = true
			}
		}
	}
	return active
}

// workoutCompleted reports whether a workout represents real, finished training:
// the user ended the session (EndedAt set) AND logged at least one set. This
// excludes the empty/abandoned rows that persist when a session is started but
// never filled in.
func workoutCompleted(w strength.Workout) bool {
	if w.EndedAt == nil {
		return false
	}
	for i := range w.Exercises {
		if len(w.Exercises[i].Sets) > 0 {
			return true
		}
	}
	return false
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
