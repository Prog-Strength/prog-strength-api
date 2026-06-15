package plannedworkout

import (
	"encoding/json"
	"errors"
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
}

func NewHandler(repo Repository, userRepo user.Repository) *Handler {
	return &Handler{repo: repo, userRepo: userRepo}
}

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
			edto.Sets = append(edto.Sets, setDTO{
				ID:           s.ID,
				OrderIndex:   s.OrderIndex,
				TargetReps:   s.TargetReps,
				TargetWeight: s.TargetWeight,
				Unit:         s.Unit,
				TargetRPE:    s.TargetRPE,
			})
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
	httpresp.OK(w, "updated planned workout", toDTO(&updated))
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
