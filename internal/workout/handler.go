package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

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
// muscle-group filter into a list of catalog exercises.
func NewHandler(repo Repository, exerciseRepo exercise.Repository) *Handler {
	return &Handler{repo: repo, exerciseRepo: exerciseRepo}
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

// unmatchSession best-effort-notifies the plan matcher that sessionID was
// deleted so any plan link is reverted. Nil matcher is a no-op.
func (h *Handler) unmatchSession(ctx context.Context, userID, sessionID string) {
	if h.planMatcher == nil {
		return
	}
	h.planMatcher.OnSessionDeleted(ctx, userID, sessionID)
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
// workout produces: one `workout` post for the session itself, plus one
// `pr` post per PR break it set (looked up by workout id). All best-effort
// and non-blocking — a failure here can never fail the workout write.
func (h *Handler) publishWorkoutPosts(ctx context.Context, w *Workout) {
	if h.publisher == nil {
		return
	}
	h.publish(ctx, timeline.PostRef{
		UserID:     w.UserID,
		SourceType: timeline.SourceWorkout,
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

// Mount registers workout routes on the given router. Callers are expected
// to have already wrapped the router in auth.RequireUser middleware — these
// handlers read the user ID from request context and assume it is present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workouts", func(r chi.Router) {
		r.Get("/", h.list)
		// Registered before any /{id} routes so chi matches literal
		// path segments instead of trying to interpret them as workout IDs.
		r.Get("/progression", h.progression)
		r.Get("/{id}", h.get)
		r.Post("/", h.create)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
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

// workoutListResponse is the data envelope for GET /workouts. Wraps
// the page of workouts in pagination metadata so the frontend can
// render "showing N–M of TOTAL" and disable Previous/Next at the
// edges of the result set.
type workoutListResponse struct {
	Items   []workoutWithEvents `json:"items"`
	Total   int                 `json:"total"`
	Limit   int                 `json:"limit"`
	Offset  int                 `json:"offset"`
	HasMore bool                `json:"has_more"`
}

// list handles GET /workouts. Returns a page of the authed user's
// workouts, most recent first.
//
// Query params (all optional):
//   - since (RFC3339): only workouts performed at or after this time
//   - until (RFC3339): only workouts performed at or before this time
//   - limit (1–100): page size, default 50
//   - offset (≥0): rows to skip, default 0
//
// Each workout in the response carries any PR events it produced via
// `personal_records_set`, so the frontend can badge sessions inline
// without a second round trip.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	opts, err := parseWorkoutListOptions(r)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	workouts, err := h.repo.ListByUser(r.Context(), userID, opts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list workouts", err)
		return
	}
	total, err := h.repo.CountByUser(r.Context(), userID, opts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count workouts", err)
		return
	}

	wrapped, err := h.attachPersonalRecordEvents(r.Context(), workouts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "fetch personal record events", err)
		return
	}
	if wrapped == nil {
		wrapped = []workoutWithEvents{}
	}

	// Echo the effective limit/offset so the caller doesn't have to
	// remember what defaults the server applied.
	effectiveLimit := opts.Limit
	if effectiveLimit <= 0 {
		effectiveLimit = 50
	}
	resp := workoutListResponse{
		Items:   wrapped,
		Total:   total,
		Limit:   effectiveLimit,
		Offset:  opts.Offset,
		HasMore: opts.Offset+len(wrapped) < total,
	}
	httpresp.OK(w, "listed workouts", resp)
}

// parseWorkoutListOptions reads pagination + timeframe query params
// off the request, validating ranges and timestamp formats. Returns
// a 400-shaped error message on bad input — the caller surfaces it.
func parseWorkoutListOptions(r *http.Request) (ListOptions, error) {
	q := r.URL.Query()
	opts := ListOptions{}

	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return opts, errors.New("invalid since: must be RFC3339 format")
		}
		opts.Since = &t
	}
	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return opts, errors.New("invalid until: must be RFC3339 format")
		}
		opts.Until = &t
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			return opts, errors.New("invalid limit: must be 1–100")
		}
		opts.Limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return opts, errors.New("invalid offset: must be ≥ 0")
		}
		opts.Offset = n
	}

	return opts, nil
}

// get handles GET /workouts/{id}. Returns a single workout owned by
// the authed user, with any PR events it produced embedded as
// `personal_records_set`. Ownership mismatches return 404 (not 403)
// so workout IDs cannot be enumerated.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	workoutID := chi.URLParam(r, "id")
	if workoutID == "" {
		httpresp.Error(w, http.StatusBadRequest, "workout id is required")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return
	}

	wrapped, err := h.attachPersonalRecordEvents(r.Context(), []Workout{*existing})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "fetch personal record events", err)
		return
	}
	httpresp.OK(w, "fetched workout", wrapped[0])
}

// progression handles GET /workouts/progression — the muscle-group
// view that powers the Progress page.
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

// create handles POST /workouts.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		// Reaching this branch means the route was mounted without
		// RequireUser middleware — a wiring bug, not a user-facing error.
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req createWorkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := req.Name
	if name == "" {
		name = fmt.Sprintf("Workout - %s", time.Now().Format("Jan 02, 2006"))
	}

	var performedAt time.Time
	var err error
	if req.PerformedAt == "" {
		performedAt = time.Now()
	} else {
		performedAt, err = time.Parse(time.RFC3339, req.PerformedAt)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid performed_at: must be RFC3339 format")
			return
		}
	}

	endedAt, err := parseOptionalRFC3339(req.EndedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid ended_at: must be RFC3339 format")
		return
	}

	workout := &Workout{
		UserID:      userID,
		Name:        name,
		PerformedAt: performedAt,
		EndedAt:     endedAt,
		Notes:       req.Notes,
		Exercises:   make([]WorkoutExercise, len(req.Exercises)),
	}
	for i, exReq := range req.Exercises {
		workout.Exercises[i] = WorkoutExercise{
			ExerciseID:    exReq.ExerciseID,
			Order:         i,
			SupersetGroup: exReq.SupersetGroup,
			Sets:          exReq.Sets,
			Notes:         exReq.Notes,
		}
	}

	if err := h.repo.Create(r.Context(), workout); err != nil {
		if mapWorkoutValidationError(w, err) {
			return
		}
		httpresp.ServerError(w, r.Context(), "create workout", err)
		return
	}

	// Best-effort: publish the workout post and any PR posts it set into
	// the timeline. Never affects this response.
	h.publishWorkoutPosts(r.Context(), workout)

	// Best-effort: link this lift to the planned workout it completes. Never
	// affects this response.
	h.matchSession(r.Context(), workout.UserID, SessionRef{SessionID: workout.ID, StartUTC: workout.PerformedAt})

	httpresp.Created(w, "created workout", workout)
}

// update handles PUT /workouts/{id}. Full-replacement semantics: the body
// contains the complete workout state (same shape as POST), and we replace
// the existing record. ID and UserID are sourced from the URL and the
// authed user respectively — values in the body are ignored.
//
// Unlike create, no convenience defaults are applied. An update means the
// caller is intentionally setting state; silently substituting a date-stamped
// name or "now" for performed_at would let a small omission clobber data
// the user didn't intend to touch. Required fields (performed_at, exercises)
// must be present.
//
// Ownership is enforced by loading the existing workout first and comparing
// UserID. Mismatches return 404 (not 403) so attackers can't enumerate
// workout IDs to discover other users' data.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	workoutID := chi.URLParam(r, "id")
	if workoutID == "" {
		httpresp.Error(w, http.StatusBadRequest, "workout id is required")
		return
	}

	// Verify ownership before accepting the body. Treat "not yours" as 404.
	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return
	}

	var req createWorkoutRequest
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.PerformedAt == "" {
		httpresp.Error(w, http.StatusBadRequest, "performed_at is required")
		return
	}
	performedAt, err := time.Parse(time.RFC3339, req.PerformedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid performed_at: must be RFC3339 format")
		return
	}

	endedAt, err := parseOptionalRFC3339(req.EndedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid ended_at: must be RFC3339 format")
		return
	}

	workout := &Workout{
		ID:          workoutID,
		UserID:      userID,
		Name:        req.Name,
		PerformedAt: performedAt,
		EndedAt:     endedAt,
		Notes:       req.Notes,
		Exercises:   make([]WorkoutExercise, len(req.Exercises)),
	}
	for i, exReq := range req.Exercises {
		workout.Exercises[i] = WorkoutExercise{
			ExerciseID:    exReq.ExerciseID,
			Order:         i,
			SupersetGroup: exReq.SupersetGroup,
			Sets:          exReq.Sets,
			Notes:         exReq.Notes,
		}
	}

	if err := h.repo.Update(r.Context(), workout); err != nil {
		if mapWorkoutValidationError(w, err) {
			return
		}
		if errors.Is(err, ErrNotFound) {
			// Race: deleted between our GetByID and Update.
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update workout", err)
		return
	}

	// Best-effort: an edit can newly set PRs (and EnsurePost is idempotent
	// for the workout post itself), so republish. Never affects this response.
	h.publishWorkoutPosts(r.Context(), workout)

	httpresp.OK(w, "updated workout", workout)
}

// delete handles DELETE /workouts/{id}. Soft-deletes the workout via the
// repo (sets DeletedAt); subsequent reads treat the row as gone. Ownership
// is enforced with a GetByID-then-compare pattern that returns 404 on
// mismatch, matching update — never reveal the existence of other users'
// workouts to ID enumeration.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	workoutID := chi.URLParam(r, "id")
	if workoutID == "" {
		httpresp.Error(w, http.StatusBadRequest, "workout id is required")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return
	}

	if err := h.repo.Delete(r.Context(), workoutID); err != nil {
		if errors.Is(err, ErrNotFound) {
			// Race: deleted between our GetByID and Delete.
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete workout", err)
		return
	}

	// Best-effort: revert any plan link for the deleted workout. Never
	// affects this response.
	h.unmatchSession(r.Context(), userID, workoutID)

	httpresp.OK(w, "deleted workout", nil)
}

// createWorkoutRequest is the request body for POST /workouts (and PUT).
type createWorkoutRequest struct {
	Name        string                  `json:"name"`
	PerformedAt string                  `json:"performed_at"` // RFC3339
	EndedAt     string                  `json:"ended_at"`     // RFC3339, optional
	Notes       string                  `json:"notes"`
	Exercises   []createWorkoutExercise `json:"exercises"`
}

type createWorkoutExercise struct {
	ExerciseID    string `json:"exercise_id"`
	SupersetGroup *int   `json:"superset_group"` // optional; nil = standalone
	Notes         string `json:"notes"`
	Sets          []Set  `json:"sets"`
}

// parseOptionalRFC3339 parses an RFC3339 timestamp string, returning nil for
// an empty string. Used for optional timestamp fields like ended_at where
// "field absent" and "field present but invalid" must be distinguished.
func parseOptionalRFC3339(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// mapWorkoutValidationError writes a 400 response if err is a known workout
// validation error and returns true. Returns false for unknown errors so the
// caller can decide (typically: log and respond 500). Centralized so create
// and update don't duplicate the long error chain.
func mapWorkoutValidationError(w http.ResponseWriter, err error) bool {
	var invalidEnumErr *InvalidEnumError
	if errors.As(err, &invalidEnumErr) || errors.Is(err, ErrUserIDRequired) ||
		errors.Is(err, ErrPerformedAtRequired) || errors.Is(err, ErrEndedAtBeforeStart) ||
		errors.Is(err, ErrExercisesRequired) || errors.Is(err, ErrExerciseIDRequired) ||
		errors.Is(err, ErrInvalidOrder) || errors.Is(err, ErrSetsRequired) ||
		errors.Is(err, ErrInvalidReps) || errors.Is(err, ErrInvalidWeight) {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return true
	}
	return false
}

// --- Personal Records --------------------------------------------------

// workoutWithEvents is the HTTP-shape wrapper that embeds the PR
// events produced by a workout into its JSON representation. Kept out
// of the Workout domain struct so the repository layer doesn't need
// to know about HTTP-only fields.
type workoutWithEvents struct {
	Workout
	PersonalRecordsSet []personalRecordEventDTO `json:"personal_records_set"`
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
