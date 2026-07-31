package strength

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// Handler exposes HTTP endpoints for workout logging.
//
// The progression endpoint needs to translate a muscle-group filter
// into the set of exercises that target it, so the handler depends on
// the exercise repository as well as the workout one. This is the only
// cross-package coupling in the workout HTTP layer — the underlying
// workout domain types and repository still have no compile-time
// dependency on `exercise` (per AGENTS.md: "Workout package doesn't
// import exercise package's data"); the join lives at the HTTP edge.
type Handler struct {
	repo         Repository
	exerciseRepo exercise.Repository
	// activityRepo owns the activities base table that holds a workout's
	// Garmin TCX enrichment (heart rate, calories, downsampled trackpoints)
	// on the workout's OWN row. The workout-TCX endpoints attach/load/detach
	// the enrichment through it. The cross-package coupling lives here at the
	// HTTP edge, mirroring exerciseRepo — the workout domain types never
	// import activity.
	activityRepo activity.Repository
	// publisher pushes completed workouts and the PR breaks they set into
	// the timeline feed index. Optional and nil-safe: existing handler
	// constructions (incl. every test) leave it nil and skip publishing, so
	// wiring it is a zero-blast-radius opt-in. Injected post-construction via
	// SetPublisher rather than through NewHandler so the constructor
	// signature — and the tests that call it — stay untouched.
	publisher timeline.Publisher
	// planMatcher best-effort links a freshly-created lift (or a deleted one)
	// to the planned workout it completes. Optional and nil-safe, mirroring
	// publisher: existing constructions leave it nil and skip matching. The
	// implementation lives in server wiring so this package never imports
	// planned_workout. Injected post-construction via SetPlanMatcher.
	planMatcher PlanMatcher
}

// NewHandler builds a Handler backed by the given repositories. The
// exercise repo is used by the progression endpoint to resolve a
// muscle-group filter into a list of catalog exercises; the activity repo
// backs the workout-TCX enrichment endpoints (import / attach / detach) and
// is loaded to embed enrichment in the workout DTOs.
func NewHandler(repo Repository, exerciseRepo exercise.Repository, activityRepo activity.Repository) *Handler {
	return &Handler{repo: repo, exerciseRepo: exerciseRepo, activityRepo: activityRepo}
}

// SetPublisher wires the timeline publisher into the handler so workout
// completion (and the PR breaks it produces) appears in the feed. Called
// from server wiring after construction. Safe to never call — publishing
// is best-effort and nil-guarded.
func (h *Handler) SetPublisher(p timeline.Publisher) { h.publisher = p }

// SetPlanMatcher wires the plan matcher in so a created workout is best-effort
// linked to the planned workout it completes (and unlinked on delete). Called
// from server wiring after construction. Safe to never call — matching is
// best-effort and nil-guarded.
func (h *Handler) SetPlanMatcher(m PlanMatcher) { h.planMatcher = m }

// matchSession best-effort-notifies the plan matcher that ref was logged. It
// NEVER affects the HTTP response: a nil matcher is a no-op.
func (h *Handler) matchSession(ctx context.Context, userID string, ref SessionRef) {
	if h.planMatcher == nil {
		return
	}
	h.planMatcher.OnSessionLogged(ctx, userID, ref)
}

// publish best-effort-publishes ref into the timeline feed index. It NEVER
// affects the HTTP response: a nil publisher is a no-op, and the publisher
// itself logs + meters any EnsurePost error and swallows it. The backfill
// repairs any gap, so a feed-index hiccup must never fail a workout save.
func (h *Handler) publish(ctx context.Context, ref timeline.PostRef) {
	if h.publisher == nil {
		return
	}
	_ = h.publisher.EnsurePost(ctx, ref)
}

// publishWorkoutPosts publishes the timeline posts a freshly-persisted
// workout produces: one `activity` post for the session itself (a lift is an
// activity like any other since the unified model — the feed reads the sport
// off activities.activity_type), plus one `pr` post per PR break it set
// (looked up by workout id). All best-effort and non-blocking — a failure
// here can never fail the workout write.
func (h *Handler) publishWorkoutPosts(ctx context.Context, w *Workout) {
	if h.publisher == nil {
		return
	}
	h.publish(ctx, timeline.PostRef{
		UserID:     w.UserID,
		SourceType: timeline.SourceActivity,
		SourceID:   w.ID,
		OccurredAt: w.PerformedAt,
	})
	// PR breaks set by this workout each get their own `pr` post, keyed by
	// the PersonalRecordEvent id with the achievement date as occurred_at.
	events, err := h.repo.ListPersonalRecordEventsByWorkouts(ctx, []string{w.ID})
	if err != nil {
		// Best-effort: a failed lookup just means the PR posts wait for the
		// next backfill/reconcile. Never surfaced to the caller.
		return
	}
	for _, e := range events {
		h.publish(ctx, timeline.PostRef{
			UserID:     e.UserID,
			SourceType: timeline.SourcePR,
			SourceID:   e.ID,
			OccurredAt: e.AchievedAt,
		})
	}
}

// Mount registers the strength surfaces that are NOT part of the unified
// /activities create/read/update path: the personal-records endpoints and the
// per-user headline-exercise customization. The former /workouts CRUD +
// progression + imports + TCX routes were removed in the stage-5 cleanup
// (unified-activity-model SOW); those handlers now mount only under /activities
// via MountActivityRoutes and the strength descriptor's Create/Update seams.
// Callers must already have wrapped the router in auth.RequireUser middleware —
// these handlers read the user ID from request context and assume it is present.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/personal-records", h.personalRecords)
	r.Get("/personal-records/{exercise_id}/history", h.exerciseOneRMHistory)

	// Per-user headline-exercise customization for the Personal
	// Records page. See prog-strength-docs/sows/custom-headline-lifts.md.
	r.Route("/me/headline-exercises", func(r chi.Router) {
		r.Get("/", h.getMyHeadlineExercises)
		r.Put("/", h.putMyHeadlineExercises)
	})
	r.Get("/headline-exercises/defaults", h.getHeadlineExercisesDefaults)
}

// MountActivityRoutes is the strength descriptor's MountRoutes: the
// type-specific endpoints on the /activities subrouter — progression, the TCX
// enrichment pair (POST|DELETE /activities/{id}/tcx), the create-from-TCX
// import, and the per-exercise 1RM history. These keep the same response shapes
// they had under the removed /workouts shims; chi matches the literal segments
// ahead of the /{id} params. Assigned to the descriptor in server.go.
func (h *Handler) MountActivityRoutes(r chi.Router) {
	r.Get("/progression", h.progression)
	r.Post("/imports", h.importFromTCX)
	r.Get("/personal-records/{exercise_id}/history", h.exerciseOneRMHistory)
	r.Post("/{id}/tcx", h.attachTCX)
	r.Delete("/{id}/tcx", h.detachTCX)
}

// CreateSession is the strength descriptor's Create seam for the unified
// POST /activities: the exact POST /workouts machinery minus the HTTP
// decode — same name default, same repository create (PR detection, 1RM
// history), same best-effort timeline posts and plan matching. Validation
// errors are wrapped in activity.ErrInvalidDetails so the unified handler's
// sentinel mapping (→ 400) works without importing this package. The
// request's vitals fields are ignored: a manually logged lift has no HR
// source — vitals arrive via TCX enrichment, exactly as on /workouts.
func (h *Handler) CreateSession(ctx context.Context, userID string, req activity.CreateRequest) (string, error) {
	w, err := h.workoutFromCreateRequest(userID, "", req)
	if err != nil {
		return "", err
	}
	if w.Name == "" {
		w.Name = fmt.Sprintf("Workout - %s", time.Now().Format("Jan 02, 2006"))
	}
	if err := h.repo.Create(ctx, w); err != nil {
		return "", translateWorkoutError(err)
	}
	h.publishWorkoutPosts(ctx, w)
	h.matchSession(ctx, userID, SessionRef{SessionID: w.ID, StartUTC: w.PerformedAt})
	return w.ID, nil
}

// UpdateSession is the strength descriptor's Update seam for the unified
// PUT /activities/{id}: full-replacement through the workout repository so
// PRs/1RM history recompute and TCX-enrichment vitals survive, exactly like
// PUT /workouts/{id} (including the best-effort PR-post republish). No name
// default, matching the workout update's "an update sets state verbatim"
// stance.
func (h *Handler) UpdateSession(ctx context.Context, userID, activityID string, req activity.CreateRequest) error {
	existing, err := h.repo.GetByID(ctx, activityID)
	if err != nil {
		return translateWorkoutError(err)
	}
	if existing.UserID != userID {
		return activity.ErrNotFound
	}
	w, err := h.workoutFromCreateRequest(userID, activityID, req)
	if err != nil {
		return err
	}
	// Carry the TCX link through for the same reason PUT /workouts does:
	// the repo's UPDATE never touches the enrichment columns.
	w.ActivityID = existing.ActivityID
	if err := h.repo.Update(ctx, w); err != nil {
		return translateWorkoutError(err)
	}
	h.publishWorkoutPosts(ctx, w)
	return nil
}

// workoutFromCreateRequest maps the unified create/update payload onto the
// domain Workout: start_time is performed_at, duration derives ended_at,
// and the details blob is the exercises list.
func (h *Handler) workoutFromCreateRequest(userID, workoutID string, req activity.CreateRequest) (*Workout, error) {
	exercises, err := parseDetails(req.Details)
	if err != nil {
		return nil, err
	}
	var endedAt *time.Time
	if req.DurationSeconds != nil {
		e := req.StartTime.Add(time.Duration(*req.DurationSeconds) * time.Second)
		endedAt = &e
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}
	return &Workout{
		ID:          workoutID,
		UserID:      userID,
		Name:        name,
		PerformedAt: req.StartTime,
		EndedAt:     endedAt,
		Notes:       notes,
		Exercises:   exercises,
	}, nil
}

// translateWorkoutError maps this package's sentinels onto the base
// activity package's, per the descriptor seam contract: not-found stays
// enumeration-safe, and the validation set reads as invalid details so the
// unified handler's errors.Is mapping yields a 400 with the real message.
func translateWorkoutError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return activity.ErrNotFound
	}
	if isWorkoutValidationError(err) {
		return fmt.Errorf("%w: %w", activity.ErrInvalidDetails, err)
	}
	return err
}

// progression handles GET /activities/progression — the muscle-group
// view that powers the Progress page. (Formerly GET /workouts/progression;
// same response shape at its canonical path since the stage-5 cleanup.)
//
// Query params (exactly one of movement_pattern / muscle_group required):
//   - movement_pattern: one of push | pull | legs | core | all. The
//     handler resolves it to its constituent muscle groups (see
//     exercise.MovementPatternMuscleGroups) and aggregates across every
//     exercise targeting any of them.
//   - muscle_group: one of the catalog muscle group enum values. The
//     handler resolves it to the set of exercises that target the group
//     and aggregates across all of them.
//   - since (optional, RFC3339): start of the date range; defaults to
//     `until - 90 days` when omitted.
//   - until (optional, RFC3339): end of the date range; defaults to
//     now when omitted.
//
// Neither param → 400 missing_filter; both → 400 conflicting_filters.
//
// Additional filter params (exercise_id, equipment, etc.) will be
// added to this endpoint over time. The intent is one progression
// endpoint that dispatches on which filter the caller provided rather
// than a separate URL per filter.
//
// For each exercise that targets the requested muscle group, the
// handler reads that exercise's full 1RM history (the table written
// by every workout CRUD), computes a recency-weighted current
// baseline, normalizes every historical entry against it, and emits
// one point per (workout, exercise) pair. The frontend plots
// everything on a single normalized axis. See
// prog-strength-docs/sows/estimated-one-rep-max.md
// for the full rationale.
func (h *Handler) progression(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	q := r.URL.Query()
	movementPatternRaw := q.Get("movement_pattern")
	muscleGroupRaw := q.Get("muscle_group")

	// Exactly one of movement_pattern / muscle_group is required.
	switch {
	case movementPatternRaw == "" && muscleGroupRaw == "":
		httpresp.ErrorWithCode(w, http.StatusBadRequest,
			"movement_pattern or muscle_group is required", "missing_filter")
		return
	case movementPatternRaw != "" && muscleGroupRaw != "":
		httpresp.ErrorWithCode(w, http.StatusBadRequest,
			"specify either movement_pattern or muscle_group, not both", "conflicting_filters")
		return
	}

	// Resolve the filter to (a) the ProgressionFilter descriptor echoed
	// in the response and (b) the ListOptions used to pull exercises.
	var filter ProgressionFilter
	var listOpts exercise.ListOptions
	if movementPatternRaw != "" {
		mp := exercise.MovementPattern(movementPatternRaw)
		if !mp.Valid() {
			httpresp.Error(w, http.StatusBadRequest, "invalid movement_pattern")
			return
		}
		groups := mp.MuscleGroups()
		filter = ProgressionFilter{
			MovementPattern: movementPatternRaw,
			MuscleGroups:    groups,
		}
		listOpts = exercise.ListOptions{MuscleGroups: groups}
	} else {
		mg := exercise.MuscleGroup(muscleGroupRaw)
		if !mg.Valid() {
			httpresp.Error(w, http.StatusBadRequest, "invalid muscle_group")
			return
		}
		filter = ProgressionFilter{
			MuscleGroup:  muscleGroupRaw,
			MuscleGroups: []exercise.MuscleGroup{mg},
		}
		listOpts = exercise.ListOptions{MuscleGroup: mg}
	}

	// `until` defaults to now; `since` defaults to until - 90 days.
	// Compute `until` first so the `since` fallback can use it.
	now := time.Now()
	until := now
	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid until: must be RFC3339 format")
			return
		}
		until = t
	}

	since := until.AddDate(0, 0, -90)
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid since: must be RFC3339 format")
			return
		}
		since = t
	}

	if !since.Before(until) {
		httpresp.Error(w, http.StatusBadRequest, "since must be before until")
		return
	}

	// Resolve the filter to its set of catalog exercises.
	exercises, err := h.exerciseRepo.List(r.Context(), listOpts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list exercises by muscle group", err)
		return
	}

	// For each exercise, pull a history window broad enough to cover
	// both the baseline computation (always last DefaultBaselineWindow)
	// and the chart points (respects since/until). Query the wider of
	// the two so a single fetch serves both purposes.
	histories := make([]ExerciseHistory, 0, len(exercises))
	historyFloor := now.Add(-DefaultBaselineWindow)
	if since.Before(historyFloor) {
		historyFloor = since
	}
	for _, ex := range exercises {
		entries, err := h.repo.ListOneRepMaxHistory(r.Context(), userID, ex.ID, &historyFloor, nil)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list one rep max history", err)
			return
		}
		if len(entries) == 0 {
			continue
		}
		histories = append(histories, ExerciseHistory{
			ExerciseID:   ex.ID,
			ExerciseName: ex.Name,
			Entries:      entries,
		})
	}

	result := ComputeMuscleGroupProgression(filter, histories, since, until, now)
	httpresp.OK(w, "computed progression", result)
}

// createWorkoutExercise is the strength descriptor's details-decode DTO: one
// exercise entry in the `details.exercises` payload of the unified POST/PUT
// /activities body (decoded via descriptor.go's detailsPayload → parseDetails;
// slice position becomes Order). Not a legacy POST /workouts leftover — the
// shims are gone, but this shape is the live wire format for strength details.
type createWorkoutExercise struct {
	ExerciseID    string `json:"exercise_id"`
	SupersetGroup *int   `json:"superset_group"` // optional; nil = standalone
	Notes         string `json:"notes"`
	Sets          []Set  `json:"sets"`
}

// isWorkoutValidationError reports whether err is one of the workout
// domain's validation sentinels. Shared by the HTTP mapping below and the
// unified-surface seam's error translation.
func isWorkoutValidationError(err error) bool {
	var invalidEnumErr *InvalidEnumError
	return errors.As(err, &invalidEnumErr) || errors.Is(err, ErrUserIDRequired) ||
		errors.Is(err, ErrPerformedAtRequired) || errors.Is(err, ErrEndedAtBeforeStart) ||
		errors.Is(err, ErrExercisesRequired) || errors.Is(err, ErrExerciseIDRequired) ||
		errors.Is(err, ErrInvalidOrder) || errors.Is(err, ErrSetsRequired) ||
		errors.Is(err, ErrInvalidReps) || errors.Is(err, ErrInvalidWeight)
}

// --- Personal Records --------------------------------------------------

// workoutWithEvents is the HTTP-shape wrapper that embeds the PR
// events produced by a workout into its JSON representation. Kept out
// of the Workout domain struct so the repository layer doesn't need
// to know about HTTP-only fields.
type workoutWithEvents struct {
	Workout
	PersonalRecordsSet []personalRecordEventDTO `json:"personal_records_set"`
	// Enrichment carries the linked activity's heart-rate / effort summary
	// (and, on the detail path, its downsampled trackpoints). Null when the
	// workout has no TCX attached (activity_id is null). On list responses
	// the trackpoints array is omitted — see toWorkoutEnrichment.
	Enrichment *workoutEnrichmentDTO `json:"enrichment"`
}

// personalRecordEventDTO is the JSON shape for a PR break event. Defined
// here (rather than as json tags on PersonalRecordEvent) so the
// nullable previous_* fields serialize as proper JSON nulls instead
// of being omitted via omitempty.
type personalRecordEventDTO struct {
	ID             string    `json:"id"`
	ExerciseID     string    `json:"exercise_id"`
	WorkoutID      string    `json:"workout_id"`
	Weight         float64   `json:"weight"`
	Reps           int       `json:"reps"`
	Unit           string    `json:"unit"`
	PreviousWeight *float64  `json:"previous_weight"`
	PreviousReps   *int      `json:"previous_reps"`
	PreviousUnit   *string   `json:"previous_unit"`
	AchievedAt     time.Time `json:"achieved_at"`
}

func eventToDTO(e PersonalRecordEvent) personalRecordEventDTO {
	dto := personalRecordEventDTO{
		ID:             e.ID,
		ExerciseID:     e.ExerciseID,
		WorkoutID:      e.WorkoutID,
		Weight:         e.Weight,
		Reps:           e.Reps,
		Unit:           string(e.Unit),
		PreviousWeight: e.PreviousWeight,
		PreviousReps:   e.PreviousReps,
		AchievedAt:     e.AchievedAt,
	}
	if e.PreviousUnit != nil {
		u := string(*e.PreviousUnit)
		dto.PreviousUnit = &u
	}
	return dto
}

// attachPersonalRecordEvents wraps a slice of workouts with their PR
// events in a single bulk fetch. Used by the workout list and detail
// handlers so the frontend can badge PR-breaking sessions inline
// without making per-workout queries.
func (h *Handler) attachPersonalRecordEvents(
	ctx context.Context,
	workouts []Workout,
) ([]workoutWithEvents, error) {
	if len(workouts) == 0 {
		return []workoutWithEvents{}, nil
	}
	ids := make([]string, len(workouts))
	for i, w := range workouts {
		ids[i] = w.ID
	}
	events, err := h.repo.ListPersonalRecordEventsByWorkouts(ctx, ids)
	if err != nil {
		return nil, err
	}
	byWorkout := make(map[string][]personalRecordEventDTO)
	for _, e := range events {
		byWorkout[e.WorkoutID] = append(byWorkout[e.WorkoutID], eventToDTO(e))
	}
	out := make([]workoutWithEvents, len(workouts))
	for i, w := range workouts {
		out[i] = workoutWithEvents{
			Workout:            w,
			PersonalRecordsSet: byWorkout[w.ID],
		}
		if out[i].PersonalRecordsSet == nil {
			out[i].PersonalRecordsSet = []personalRecordEventDTO{}
		}
	}
	return out, nil
}

// personalRecordDTO is the JSON shape for one row of the
// /personal-records response. Carries the PR row's fields plus the
// computed current_estimated_1rm for the same exercise. Nullable
// fields use pointers so JSON renders null rather than zero values.
type personalRecordDTO struct {
	ExerciseID          string     `json:"exercise_id"`
	ExerciseName        string     `json:"exercise_name"`
	WorkoutID           *string    `json:"workout_id"`
	Weight              *float64   `json:"weight"`
	Reps                *int       `json:"reps"`
	Unit                *string    `json:"unit"`
	AchievedAt          *time.Time `json:"achieved_at"`
	CurrentEstimated1RM *float64   `json:"current_estimated_1rm"`
	Estimated1RMUnit    *string    `json:"estimated_1rm_unit"`
	// RecentEstimated1RMPoints is a compact, downsampled ascending
	// (oldest→newest) trend of the estimated 1RM, for the list view's
	// per-tile spark. Built from the same in-window history entries used
	// for CurrentEstimated1RM — no extra query. null when no history.
	RecentEstimated1RMPoints []float64 `json:"recent_estimated_1rm_points"`
}

// recentEstimated1RMPointCap bounds the per-tile trend spark on the
// /personal-records list row so a long-history lift doesn't bloat the
// response. ~10 points is enough to read as a trend.
const recentEstimated1RMPointCap = 10

// personalRecords handles GET /personal-records.
//
// Returns one row per headline exercise in the user's selection
// order. Headline exercises the user has never trained still appear,
// with PR fields set to null — the frontend renders empty-state
// cards from these rows. The current_estimated_1rm field is computed
// per request from the 1RM history table; it is not stored.
//
// The slug list comes from effectiveHeadlineExerciseSlugs, which
// consults user_headline_exercises first and falls back to the
// curated HeadlineExercises default when the user hasn't customized.
func (h *Handler) personalRecords(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	slugs, err := h.effectiveHeadlineExerciseSlugs(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list user headline exercises", err)
		return
	}

	// Build a quick lookup of the user's existing PRs.
	prs, err := h.repo.ListPersonalRecords(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list personal records", err)
		return
	}
	byExercise := make(map[string]PersonalRecord, len(prs))
	for _, pr := range prs {
		byExercise[pr.ExerciseID] = pr
	}

	// Resolve exercise display names from the catalog.
	exerciseNames := make(map[string]string, len(slugs))
	for _, slug := range slugs {
		ex, err := h.exerciseRepo.GetByID(r.Context(), slug)
		if err == nil {
			exerciseNames[slug] = ex.Name
		} else {
			// Catalog mismatch — the unit test guards the defaults, but
			// a user could pin an exercise that gets removed from the
			// catalog later; fall back to the slug so the row still
			// renders something rather than crashing the request.
			exerciseNames[slug] = slug
		}
	}

	now := time.Now()
	until := now
	since := until.Add(-DefaultBaselineWindow)

	out := make([]personalRecordDTO, 0, len(slugs))
	for _, slug := range slugs {
		dto := personalRecordDTO{
			ExerciseID:   slug,
			ExerciseName: exerciseNames[slug],
		}

		if pr, ok := byExercise[slug]; ok {
			weight := pr.Weight
			reps := pr.Reps
			unit := string(pr.Unit)
			workoutID := pr.WorkoutID
			achievedAt := pr.AchievedAt
			dto.Weight = &weight
			dto.Reps = &reps
			dto.Unit = &unit
			dto.WorkoutID = &workoutID
			dto.AchievedAt = &achievedAt
		}

		// Compute the current recency-weighted estimated 1RM from the
		// 1RM history table. Not stored; cheap to compute on demand.
		entries, err := h.repo.ListOneRepMaxHistory(r.Context(), userID, slug, &since, &until)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list one rep max history", err)
			return
		}
		if baseline, ok := RecencyWeightedBaseline(entries, now, DefaultBaselineWindow, DefaultBaselineTau); ok {
			rounded := round1(baseline)
			dto.CurrentEstimated1RM = &rounded
			// Unit of the baseline mirrors the most-recent in-window
			// entry's unit, which is the same convention used in the
			// muscle-group progression handler.
			for _, e := range entries {
				if !e.PerformedAt.Before(now.Add(-DefaultBaselineWindow)) {
					u := string(e.Unit)
					dto.Estimated1RMUnit = &u
					break
				}
			}
		}

		// Per-tile trend spark: a downsampled, ascending series of the
		// same in-window history entries already fetched for the
		// baseline above. Capped small so a long-history lift doesn't
		// bloat the row; nil (JSON null) when there's no history.
		dto.RecentEstimated1RMPoints = DownsampleEstimated1RM(entries, recentEstimated1RMPointCap)

		out = append(out, dto)
	}

	httpresp.OK(w, "listed personal records", out)
}

// --- Per-exercise 1RM history ------------------------------------------

// exerciseOneRMHistoryPointDTO is one point in the per-exercise estimated
// 1RM time series: the max estimated 1RM across that workout's sets on the
// exercise, at the workout's performed_at.
type exerciseOneRMHistoryPointDTO struct {
	WorkoutID    string    `json:"workout_id"`
	PerformedAt  time.Time `json:"performed_at"`
	Estimated1RM float64   `json:"estimated_1rm"`
}

// exerciseOneRMHistoryResponse is the GET
// /personal-records/{exercise_id}/history payload — one point per workout
// the exercise was performed in, ascending by performed_at.
type exerciseOneRMHistoryResponse struct {
	ExerciseID   string                         `json:"exercise_id"`
	ExerciseName string                         `json:"exercise_name"`
	Unit         *string                        `json:"unit"`
	Points       []exerciseOneRMHistoryPointDTO `json:"points"`
}

// exerciseOneRMHistory handles GET /personal-records/{exercise_id}/history.
//
// Returns the per-workout estimated 1RM series for a single exercise,
// derived from exercise_one_rep_max_history — the per-exercise slice that
// feeds the personal-records page's progression chart, separate from the
// muscle-group rollup. One point per workout (max estimated 1RM across that
// workout's sets), ascending by performed_at. The unit mirrors the most
// recent entry; it's null when the user has no history for the exercise.
//
// A slug not in the exercise catalog is a 404 with code
// unknown_exercise_id, matching the personalRecords catalog-lookup pattern.
func (h *Handler) exerciseOneRMHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	exerciseID := chi.URLParam(r, "exercise_id")
	if exerciseID == "" {
		httpresp.Error(w, http.StatusBadRequest, "exercise id is required")
		return
	}

	ex, err := h.exerciseRepo.GetByID(r.Context(), exerciseID)
	if err != nil {
		if errors.Is(err, exercise.ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "unknown exercise", "unknown_exercise_id")
			return
		}
		httpresp.ServerError(w, r.Context(), "get exercise", err)
		return
	}

	// Full series, no date bounds. ListOneRepMaxHistory returns one row per
	// (workout, exercise) carrying max_estimated_1rm, ordered DESC by
	// performed_at; we emit ascending so a line chart consumes it directly.
	entries, err := h.repo.ListOneRepMaxHistory(r.Context(), userID, exerciseID, nil, nil)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list one rep max history", err)
		return
	}

	resp := exerciseOneRMHistoryResponse{
		ExerciseID:   ex.ID,
		ExerciseName: ex.Name,
		Points:       make([]exerciseOneRMHistoryPointDTO, 0, len(entries)),
	}
	// entries are DESC by performed_at; the unit mirrors the most recent
	// entry (the first one), matching the personalRecords convention.
	if len(entries) > 0 {
		u := string(entries[0].Unit)
		resp.Unit = &u
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		resp.Points = append(resp.Points, exerciseOneRMHistoryPointDTO{
			WorkoutID:    e.WorkoutID,
			PerformedAt:  e.PerformedAt,
			Estimated1RM: round1(e.MaxEstimated1RM),
		})
	}

	httpresp.OK(w, "listed exercise one rep max history", resp)
}
