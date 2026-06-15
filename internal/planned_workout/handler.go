package plannedworkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Handler serves /planned-workouts: scheduled training entries with an
// optional lift agenda. POST creates, GET lists/reads, PUT edits (overlaying
// supplied fields and replacing the agenda when exercises are present),
// DELETE soft-deletes, and POST /{id}/skip transitions the plan to "skipped".
//
// The handler depends on the user repository to default a plan's timezone to
// the user's Timezone when the request omits it — the same convention
// bodyweight uses to default the weight unit.
type Handler struct {
	repo     Repository
	userRepo user.Repository
	// calendar pushes plans to Google Calendar. Optional and nil-safe:
	// constructions that don't wire it (every test, and the server when
	// calendar sync isn't configured) leave it nil. The /schedule and /resync
	// endpoints then return 503; create/update/delete simply skip the
	// best-effort push. Injected post-construction via SetCalendarSync so
	// NewHandler's signature — and its existing callers — stay untouched
	// (mirrors workoutHandler.SetPublisher).
	calendar CalendarScheduler
}

// CalendarScheduler is the planned-workout view of the calendar sync service:
// push a plan to Google (Schedule/Resync) or remove its event (Delete). Kept as
// an interface here so the handler stays decoupled from the calendarsync
// package (which itself imports plannedworkout — wiring the concrete type in
// would create an import cycle).
type CalendarScheduler interface {
	Schedule(ctx context.Context, userID, planID, detailOverride string) error
	Resync(ctx context.Context, userID, planID string) error
	Delete(ctx context.Context, userID, planID string) error
	// RewriteCompleted patches the plan's Google event to show actual logged
	// details plus a "completed" marker. Called from the /complete flow; the
	// calendarsync.Service already implements it. Best-effort at the call site:
	// the handler logs a failure but does not fail the request.
	RewriteCompleted(ctx context.Context, userID, planID, actualText string) error
}

func NewHandler(repo Repository, userRepo user.Repository) *Handler {
	return &Handler{repo: repo, userRepo: userRepo}
}

// SetCalendarSync wires the Google Calendar scheduler into the handler. Called
// from server wiring after construction, only when calendar sync is configured.
// Safe to never call — the /schedule and /resync routes nil-guard to a clear
// 503 and the create/update/delete push is best-effort.
func (h *Handler) SetCalendarSync(s CalendarScheduler) { h.calendar = s }

// Mount registers routes on the given router. Callers are expected to have
// already wrapped the router in auth.RequireUser — these handlers read the
// user ID out of request context.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/planned-workouts", func(r chi.Router) {
		r.Post("/", h.create)
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
		r.Post("/{id}/skip", h.skip)
		r.Post("/{id}/complete", h.complete)
		r.Post("/{id}/schedule", h.schedule)
		r.Post("/{id}/resync", h.resync)
	})
}

// --- DTOs ----------------------------------------------------------

// planDTO is the JSON shape of a planned workout. Nullable fields use
// pointers so they serialize as null (or are omitted via omitempty)
// consistently with the other domains rather than rendering zero values.
type planDTO struct {
	ID           string  `json:"id"`
	Name         *string `json:"name"`
	ActivityKind string  `json:"activity_kind"`

	ScheduledStart time.Time `json:"scheduled_start"`
	ScheduledEnd   time.Time `json:"scheduled_end"`
	Timezone       string    `json:"timezone"`

	Status string  `json:"status"`
	Notes  *string `json:"notes"`

	CompletedSessionID   *string `json:"completed_session_id"`
	CompletedSessionKind *string `json:"completed_session_kind"`

	CalendarDetail *string `json:"calendar_detail"`

	GoogleEventID    *string `json:"google_event_id"`
	GoogleSyncStatus *string `json:"google_sync_status"`
	LastSyncError    *string `json:"last_sync_error"`

	Exercises []exerciseDTO `json:"exercises"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type exerciseDTO struct {
	ID         string   `json:"id"`
	ExerciseID string   `json:"exercise_id"`
	OrderIndex int      `json:"order_index"`
	Notes      *string  `json:"notes"`
	Sets       []setDTO `json:"sets"`
}

type setDTO struct {
	ID           string   `json:"id"`
	OrderIndex   int      `json:"order_index"`
	TargetReps   *int     `json:"target_reps"`
	TargetWeight *float64 `json:"target_weight"`
	Unit         *string  `json:"unit"`
	TargetRPE    *float64 `json:"target_rpe"`
}

func toDTO(pw *PlannedWorkout) planDTO {
	dto := planDTO{
		ID:                 pw.ID,
		Name:               pw.Name,
		ActivityKind:       string(pw.ActivityKind),
		ScheduledStart:     pw.ScheduledStartUTC,
		ScheduledEnd:       pw.ScheduledEndUTC,
		Timezone:           pw.Timezone,
		Status:             string(pw.Status),
		Notes:              pw.Notes,
		CompletedSessionID: pw.CompletedSessionID,
		GoogleEventID:      pw.GoogleEventID,
		LastSyncError:      pw.LastSyncError,
		Exercises:          make([]exerciseDTO, 0, len(pw.Exercises)),
		CreatedAt:          pw.CreatedAt,
		UpdatedAt:          pw.UpdatedAt,
	}
	if pw.CompletedSessionKind != nil {
		k := string(*pw.CompletedSessionKind)
		dto.CompletedSessionKind = &k
	}
	if pw.CalendarDetail != nil {
		d := string(*pw.CalendarDetail)
		dto.CalendarDetail = &d
	}
	if pw.GoogleSyncStatus != nil {
		s := string(*pw.GoogleSyncStatus)
		dto.GoogleSyncStatus = &s
	}
	for _, ex := range pw.Exercises {
		edto := exerciseDTO{
			ID:         ex.ID,
			ExerciseID: ex.ExerciseID,
			OrderIndex: ex.OrderIndex,
			Notes:      ex.Notes,
			Sets:       make([]setDTO, 0, len(ex.Sets)),
		}
		for _, s := range ex.Sets {
			edto.Sets = append(edto.Sets, setDTO(s))
		}
		dto.Exercises = append(dto.Exercises, edto)
	}
	return dto
}

// planRequest is the create/update body. Pointer fields distinguish
// "absent" (nil) from "present" so PUT can overlay only the supplied fields.
// scheduled_start/scheduled_end are RFC3339 strings; *string lets create
// require them (nil → 400) while update treats nil as "unchanged".
type planRequest struct {
	Name           *string        `json:"name"`
	ScheduledStart *string        `json:"scheduled_start"`
	ScheduledEnd   *string        `json:"scheduled_end"`
	Timezone       *string        `json:"timezone"`
	Notes          *string        `json:"notes"`
	CalendarDetail *string        `json:"calendar_detail"`
	Exercises      *[]exerciseReq `json:"exercises"`
	// CalendarSync, when true, also pushes the plan to Google Calendar after a
	// successful DB write. It is the "sync now" toggle, distinct from
	// calendar_detail (which controls how much agenda the event carries). The
	// push is best-effort: a Google failure never fails create/update — the
	// resulting sync status is reflected in the response instead.
	CalendarSync *bool `json:"calendar_sync"`
}

type exerciseReq struct {
	ExerciseID string   `json:"exercise_id"`
	Notes      *string  `json:"notes"`
	Sets       []setReq `json:"sets"`
}

type setReq struct {
	TargetReps   *int     `json:"target_reps"`
	TargetWeight *float64 `json:"target_weight"`
	Unit         *string  `json:"unit"`
	TargetRPE    *float64 `json:"target_rpe"`
}

// buildExercises maps the request agenda to domain PlannedExercise/PlannedSet
// slices. order_index is derived from array position by the repository, so
// it's left zero here (mirrors how the workout handler builds exercises).
func buildExercises(reqs []exerciseReq) []PlannedExercise {
	out := make([]PlannedExercise, len(reqs))
	for i, ex := range reqs {
		pe := PlannedExercise{
			ExerciseID: ex.ExerciseID,
			Notes:      ex.Notes,
			Sets:       make([]PlannedSet, len(ex.Sets)),
		}
		for j, s := range ex.Sets {
			pe.Sets[j] = PlannedSet{
				TargetReps:   s.TargetReps,
				TargetWeight: s.TargetWeight,
				Unit:         s.Unit,
				TargetRPE:    s.TargetRPE,
			}
		}
		out[i] = pe
	}
	return out
}

// --- Handlers ------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ScheduledStart == nil || req.ScheduledEnd == nil {
		httpresp.Error(w, http.StatusBadRequest, "scheduled_start and scheduled_end are required")
		return
	}
	start, err := time.Parse(time.RFC3339, *req.ScheduledStart)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid scheduled_start: must be RFC3339 format")
		return
	}
	end, err := time.Parse(time.RFC3339, *req.ScheduledEnd)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid scheduled_end: must be RFC3339 format")
		return
	}

	// Timezone defaults to the user's Timezone when the client omits it. We
	// do the lookup here rather than push it onto every caller; on a repo
	// error fall back to UTC so the request doesn't 500.
	timezone := "UTC"
	if req.Timezone != nil && *req.Timezone != "" {
		timezone = *req.Timezone
	} else if u, err := h.userRepo.GetByID(r.Context(), userID); err == nil {
		timezone = u.Timezone
	}

	pw := &PlannedWorkout{
		UserID:            userID,
		Name:              req.Name,
		ActivityKind:      ActivityKindLift,
		ScheduledStartUTC: start.UTC(),
		ScheduledEndUTC:   end.UTC(),
		Timezone:          timezone,
		Status:            StatusPlanned,
		Notes:             req.Notes,
		CalendarDetail:    toCalendarDetail(req.CalendarDetail),
	}
	if req.Exercises != nil {
		pw.Exercises = buildExercises(*req.Exercises)
	}

	if err := h.repo.Create(r.Context(), pw); err != nil {
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create planned workout", err)
		return
	}

	// Optional "sync now": best-effort push to Google Calendar. A write failure
	// (or a connection problem) never fails the create — the plan is already
	// persisted. We reflect the resulting sync status by re-reading the plan
	// (Schedule persists status/event id onto it) before rendering the response.
	if req.CalendarSync != nil && *req.CalendarSync && h.calendar != nil {
		_ = h.calendar.Schedule(r.Context(), userID, pw.ID, "")
		if refreshed, rerr := h.repo.Get(r.Context(), userID, pw.ID); rerr == nil {
			pw = refreshed
		}
	}
	httpresp.Created(w, "planned workout created", toDTO(pw))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	since, until, err := parseSinceUntil(r)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	plans, err := h.repo.List(r.Context(), userID, since, until)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list planned workouts", err)
		return
	}
	out := make([]planDTO, 0, len(plans))
	for i := range plans {
		out = append(out, toDTO(&plans[i]))
	}
	httpresp.OK(w, "listed planned workouts", out)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}
	pw, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}
	httpresp.OK(w, "fetched planned workout", toDTO(pw))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}

	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}

	updated := *existing
	if req.Name != nil {
		updated.Name = req.Name
	}
	if req.Notes != nil {
		updated.Notes = req.Notes
	}
	if req.Timezone != nil {
		updated.Timezone = *req.Timezone
	}
	if req.CalendarDetail != nil {
		updated.CalendarDetail = toCalendarDetail(req.CalendarDetail)
	}
	if req.ScheduledStart != nil {
		start, err := time.Parse(time.RFC3339, *req.ScheduledStart)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid scheduled_start: must be RFC3339 format")
			return
		}
		updated.ScheduledStartUTC = start.UTC()
	}
	if req.ScheduledEnd != nil {
		end, err := time.Parse(time.RFC3339, *req.ScheduledEnd)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid scheduled_end: must be RFC3339 format")
			return
		}
		updated.ScheduledEndUTC = end.UTC()
	}
	// Replace the agenda only when the request includes the exercises key.
	// Absent → keep existing; present (incl. empty array) → replace.
	if req.Exercises != nil {
		updated.Exercises = buildExercises(*req.Exercises)
	}

	if err := h.repo.Update(r.Context(), &updated); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "update planned workout", err)
		return
	}

	// Optional "sync now" on update — same best-effort semantics as create.
	// Re-read so the response reflects the persisted sync status/event id.
	resp := &updated
	if req.CalendarSync != nil && *req.CalendarSync && h.calendar != nil {
		_ = h.calendar.Schedule(r.Context(), userID, updated.ID, "")
		if refreshed, rerr := h.repo.Get(r.Context(), userID, updated.ID); rerr == nil {
			resp = refreshed
		}
	}
	httpresp.OK(w, "updated planned workout", toDTO(resp))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}
	// Best-effort: remove the Google Calendar event BEFORE the soft-delete so
	// the plan (and its event id) is still loadable by the scheduler. A failure
	// here never blocks the plan delete — an orphaned event is recoverable, a
	// lost delete is not. Skipped entirely when calendar sync isn't wired.
	if h.calendar != nil {
		_ = h.calendar.Delete(r.Context(), userID, id)
	}

	if err := h.repo.Delete(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete planned workout", err)
		return
	}
	httpresp.OK(w, "planned workout deleted", nil)
}

func (h *Handler) skip(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}
	if err := h.repo.SetStatus(r.Context(), userID, id, StatusSkipped); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "skip planned workout", err)
		return
	}
	httpresp.OK(w, "planned workout skipped", nil)
}

// completeRequest is the body for POST /{id}/complete: the polymorphic link to
// the session that fulfilled the plan. Both fields are required.
type completeRequest struct {
	SessionID   string `json:"session_id"`
	SessionKind string `json:"session_kind"`
}

// complete (POST /{id}/complete) flips a plan to "completed", stores the
// polymorphic link to the logged session (a workout or an activity), and — when
// the plan is Google-synced — best-effort rewrites its calendar event to show
// the actuals plus a completed marker.
func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}

	var req completeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.SessionKind == "" {
		httpresp.Error(w, http.StatusBadRequest, "session_id and session_kind are required")
		return
	}
	// Validate the kind here for a clean message; SetCompletion → Validate would
	// otherwise surface the same as ErrInvalidCompletionLink.
	kind := SessionKind(req.SessionKind)
	if kind != SessionKindWorkout && kind != SessionKindActivity {
		httpresp.Error(w, http.StatusBadRequest, "session_kind must be 'workout' or 'activity'")
		return
	}

	// Confirm the plan exists/is owned before mutating, so a bad id is a clean
	// 404 (and the same for cross-user, which the repo collapses to ErrNotFound).
	plan, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}

	if err = h.repo.SetCompletion(r.Context(), userID, id, req.SessionID, kind); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "complete planned workout", err)
		return
	}

	// If the plan was pushed to Google, rewrite the event to reflect completion.
	// Best-effort: a Google failure is logged but never fails the request — the
	// plan is already marked completed in our store. v1 actualText is a concise
	// marker referencing the linked session; the handler only has the session
	// id/kind, not the full logged details (see SOW note).
	if plan.GoogleEventID != nil && *plan.GoogleEventID != "" && h.calendar != nil {
		actualText := "Completed — logged " + req.SessionKind + " session " + req.SessionID
		if err = h.calendar.RewriteCompleted(r.Context(), userID, id, actualText); err != nil {
			log.Printf("complete planned workout: rewrite google event (plan %s): %v", id, err)
		}
	}

	// Re-read so the response reflects the completed status, the populated link,
	// and any sync status the rewrite persisted.
	updated, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}
	httpresp.OK(w, "planned workout completed", toDTO(updated))
}

// scheduleRequest is the body for POST /{id}/schedule and /{id}/resync. The
// detail level is optional; "" means use the plan's calendar_detail, then the
// user's default.
type scheduleRequest struct {
	DetailLevel *string `json:"detail_level"`
}

// schedule (POST /{id}/schedule) pushes the plan to Google Calendar at the
// requested (or defaulted) detail level. The Google write is best-effort: a
// missing/revoked connection maps to 409 (the user must act), but an actual
// write failure does NOT 5xx and does NOT lose the plan — we re-read the plan
// and return 200 with its (now "failed") sync status so the client can show it.
func (h *Handler) schedule(w http.ResponseWriter, r *http.Request) {
	h.syncEndpoint(w, r, false)
}

// resync (POST /{id}/resync) re-attempts the last write for a plan. Same shape
// and mapping as schedule.
func (h *Handler) resync(w http.ResponseWriter, r *http.Request) {
	h.syncEndpoint(w, r, true)
}

// syncEndpoint is the shared body of schedule/resync. When isResync is false it
// honors the optional detail_level override; resync ignores the body (the
// render is deterministic from the plan) and just re-runs the write.
func (h *Handler) syncEndpoint(w http.ResponseWriter, r *http.Request, isResync bool) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "planned workout id is required")
		return
	}
	if h.calendar == nil {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "calendar sync not configured", "calendar_sync_unconfigured")
		return
	}

	// Optional detail override (schedule only). An empty body is fine.
	detail := ""
	if !isResync {
		var req scheduleRequest
		// An empty body (EOF) is valid: detail defaults to the plan/user
		// preference. Only a malformed non-empty body is a 400.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpresp.Error(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.DetailLevel != nil {
			detail = *req.DetailLevel
		}
	}

	// Confirm the plan exists/is owned before attempting a write, so a bad id
	// is a clean 404 rather than surfacing through the scheduler.
	if _, err := h.repo.Get(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}

	var syncErr error
	if isResync {
		syncErr = h.calendar.Resync(r.Context(), userID, id)
	} else {
		syncErr = h.calendar.Schedule(r.Context(), userID, id, detail)
	}

	// Connection problems require the user to act → 409 with a code. These are
	// NOT best-effort: there's nothing to retry until the user reconnects.
	if errors.Is(syncErr, ErrCalendarNotConnected) {
		httpresp.ErrorWithCode(w, http.StatusConflict, "calendar not connected; connect your Google Calendar first", "calendar_not_connected")
		return
	}
	if errors.Is(syncErr, ErrCalendarReconnectNeeded) {
		httpresp.ErrorWithCode(w, http.StatusConflict, "calendar connection needs to be reconnected", "calendar_reconnect_needed")
		return
	}

	// Any other outcome (success OR a best-effort write failure): re-read the
	// plan and return 200 with its current sync status. The Google write being
	// best-effort means a failed write is reflected in google_sync_status =
	// "failed", NOT a 5xx — the plan is never lost.
	pw, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "planned workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get planned workout", err)
		return
	}
	httpresp.OK(w, "planned workout calendar sync attempted", toDTO(pw))
}

// --- helpers -------------------------------------------------------

// toCalendarDetail converts an optional request string into the domain
// CalendarDetail pointer. An invalid value is passed through as-is so
// Validate surfaces ErrInvalidCalendarDetail → clean 400 (rather than
// being silently dropped here).
func toCalendarDetail(s *string) *CalendarDetail {
	if s == nil {
		return nil
	}
	d := CalendarDetail(*s)
	return &d
}

func parseSinceUntil(r *http.Request) (*time.Time, *time.Time, error) {
	var since, until *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("invalid since (expected RFC3339)")
		}
		since = &t
	}
	if raw := r.URL.Query().Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("invalid until (expected RFC3339)")
		}
		until = &t
	}
	if since != nil && until != nil && !since.Before(*until) {
		return nil, nil, errors.New("since must be strictly before until")
	}
	return since, until, nil
}
