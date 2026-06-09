package running

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// maxTCXBytes caps the multipart upload size. A typical run TCX is a few
// hundred KB; 10 MB is generous headroom while bounding memory per import
// (the whole file is read into a byte slice for parse + archive).
const maxTCXBytes = 10 << 20

// listLimitDefault / listLimitMax bound the page size for GET /sessions.
const (
	listLimitDefault = 50
	listLimitMax     = 100
)

// Handler exposes the HTTP surface for running sessions: TCX import, the
// list/detail/rename/delete CRUD, and the dashboard metrics tiles.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// Mount registers routes under /running. Callers are expected to have
// already wrapped the router in auth.RequireUser — these handlers read the
// user ID from request context and assume it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/running", func(r chi.Router) {
		r.Post("/sessions/imports", h.importSession)
		r.Get("/sessions", h.listSessions)
		r.Get("/sessions/{id}", h.getSession)
		r.Patch("/sessions/{id}", h.renameSession)
		r.Delete("/sessions/{id}", h.deleteSession)
		r.Get("/metrics", h.metrics)
	})
}

// --- DTOs ----------------------------------------------------------

// trackpointDTO is one downsampled chart point. Nullable signals (HR,
// pace, elevation) keep their keys present rendering null when absent so
// the client can branch on presence rather than guess.
type trackpointDTO struct {
	Sequence        int      `json:"sequence"`
	ElapsedSeconds  int      `json:"elapsed_seconds"`
	DistanceMeters  float64  `json:"distance_meters"`
	HeartRateBpm    *int     `json:"heart_rate_bpm"`
	PaceSecPerKm    *float64 `json:"pace_sec_per_km"`
	ElevationMeters *float64 `json:"elevation_meters"`
}

// sessionDTO is the wire shape of a running session. Nullable numerics are
// pointers WITHOUT omitempty so the key is always present (rendered null
// when nil) — the client relies on a stable key set. Trackpoints is the
// one exception: omitempty drops it on list responses, which never load
// the per-point stream.
type sessionDTO struct {
	ID                  string          `json:"id"`
	GarminActivityID    string          `json:"garmin_activity_id"`
	Name                *string         `json:"name"`
	StartTime           time.Time       `json:"start_time"`
	DistanceMeters      float64         `json:"distance_meters"`
	DurationSeconds     int             `json:"duration_seconds"`
	AvgPaceSecPerKm     float64         `json:"avg_pace_sec_per_km"`
	BestPaceSecPerKm    *float64        `json:"best_pace_sec_per_km"`
	AvgHeartRateBpm     *int            `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm     *int            `json:"max_heart_rate_bpm"`
	TotalCalories       *int            `json:"total_calories"`
	ElevationGainMeters *float64        `json:"elevation_gain_meters"`
	CreatedAt           time.Time       `json:"created_at"`
	Trackpoints         []trackpointDTO `json:"trackpoints,omitempty"`
}

func toSessionDTO(s Session, withTrackpoints bool) sessionDTO {
	dto := sessionDTO{
		ID:                  s.ID,
		GarminActivityID:    s.GarminActivityID,
		Name:                s.Name,
		StartTime:           s.StartTime,
		DistanceMeters:      s.DistanceMeters,
		DurationSeconds:     s.DurationSeconds,
		AvgPaceSecPerKm:     s.AvgPaceSecPerKm,
		BestPaceSecPerKm:    s.BestPaceSecPerKm,
		AvgHeartRateBpm:     s.AvgHeartRateBpm,
		MaxHeartRateBpm:     s.MaxHeartRateBpm,
		TotalCalories:       s.TotalCalories,
		ElevationGainMeters: s.ElevationGainMeters,
		CreatedAt:           s.CreatedAt,
	}
	if withTrackpoints {
		dto.Trackpoints = make([]trackpointDTO, 0, len(s.Trackpoints))
		for _, tp := range s.Trackpoints {
			dto.Trackpoints = append(dto.Trackpoints, trackpointDTO(tp))
		}
	}
	return dto
}

// listResponse is the GET /sessions payload: a page of sessions plus the
// keyset cursor for the next page. NextBefore is null when this is the
// last page (fewer than limit returned).
type listResponse struct {
	Sessions   []sessionDTO `json:"sessions"`
	NextBefore *string      `json:"next_before"`
}

// metricsResponse and its sub-shapes mirror the SOW JSON for the dashboard
// tiles. Nullable rollup fields stay present-as-null (no omitempty).
type periodStatDTO struct {
	DistanceMeters float64 `json:"distance_meters"`
	RunCount       int     `json:"run_count"`
}

type currentWeekDTO struct {
	DistanceMeters      float64  `json:"distance_meters"`
	RunCount            int      `json:"run_count"`
	DeltaPctVsPriorWeek *float64 `json:"delta_pct_vs_prior_week"`
}

type metricsResponse struct {
	CurrentWeek           currentWeekDTO `json:"current_week"`
	CurrentMonth          periodStatDTO  `json:"current_month"`
	RecentAvgPaceSecPerKm *float64       `json:"recent_avg_pace_sec_per_km"`
	AllTime               periodStatDTO  `json:"all_time"`
}

// --- handlers ------------------------------------------------------

// importSession parses a multipart-uploaded Garmin TCX file, validates it
// is a non-empty run, summarizes it, and persists the session + archived
// file. Failure modes carry machine-readable codes (see ErrorWithCode) so
// the import client can show a precise reason.
func (h *Handler) importSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	// Cap the body before reading anything. MaxBytesReader makes the read
	// error out (and sets a 413-appropriate flag) once the cap is exceeded,
	// so a malicious huge upload can't exhaust memory.
	r.Body = http.MaxBytesReader(w, r.Body, maxTCXBytes)
	if err := r.ParseMultipartForm(maxTCXBytes); err != nil {
		// MaxBytesReader signals an overflow via *http.MaxBytesError.
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return
		}
		// Anything else (not multipart, malformed form) is an unsupported
		// upload — we require a multipart form with a file field.
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "expected a multipart upload with a file field", "unsupported_media_type")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "missing file field in multipart upload", "unsupported_media_type")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		// A read overflow here also surfaces as MaxBytesError (the cap
		// applies to the whole body, parts included).
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return
		}
		httpresp.ServerError(w, r.Context(), "read uploaded tcx", err)
		return
	}

	rid := requestid.FromContext(r.Context())

	parsed, err := parseTCX(data)
	if err != nil {
		log.Printf("running import: request_id=%s user_id=%s garmin_activity_id=%s outcome=invalid slug=%s", rid, userID, "", SlugParseFailed)
		httpresp.ErrorWithCode(w, http.StatusBadRequest, err.Error(), SlugParseFailed)
		return
	}

	if err := validate(parsed); err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			log.Printf("running import: request_id=%s user_id=%s garmin_activity_id=%s outcome=invalid slug=%s", rid, userID, parsed.ActivityID, verr.Slug)
			httpresp.ErrorWithCode(w, http.StatusBadRequest, verr.Msg, verr.Slug)
			return
		}
		httpresp.ServerError(w, r.Context(), "validate tcx", err)
		return
	}

	s := summarize(parsed)
	s.UserID = userID

	switch err := h.repo.Create(r.Context(), &s, data); {
	case err == nil:
		log.Printf("running import: request_id=%s user_id=%s garmin_activity_id=%s outcome=imported", rid, userID, s.GarminActivityID)
		httpresp.Created(w, "imported running session", toSessionDTO(s, true))
	case errors.Is(err, ErrDuplicate):
		log.Printf("running import: request_id=%s user_id=%s garmin_activity_id=%s outcome=duplicate", rid, userID, s.GarminActivityID)
		existingID := ""
		if existing, lookupErr := h.repo.GetByGarminActivityID(r.Context(), userID, s.GarminActivityID); lookupErr == nil {
			existingID = existing.ID
		}
		httpresp.ErrorWithCodeData(w, http.StatusConflict, "a session for this activity already exists", "duplicate_run", map[string]any{
			"existing_session_id": existingID,
		})
	case errors.Is(err, ErrStorage):
		log.Printf("running import: request_id=%s user_id=%s garmin_activity_id=%s outcome=storage_failed err=%v", rid, userID, s.GarminActivityID, err)
		httpresp.ErrorWithCode(w, http.StatusInternalServerError, "failed to archive tcx file", "storage_failed")
	default:
		httpresp.ServerError(w, r.Context(), "create running session", err)
	}
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	// Two mutually exclusive query patterns share this endpoint:
	//   - cursor (limit, before): newest-first page, returns next_before
	//   - range  (since, until):  half-open window [since, until), no cursor
	// The calendar's month-view uses range; the run list uses cursor.
	hasSince := r.URL.Query().Get("since") != ""
	hasUntil := r.URL.Query().Get("until") != ""
	hasBefore := r.URL.Query().Get("before") != ""
	hasLimit := r.URL.Query().Get("limit") != ""
	if (hasSince || hasUntil) && (hasBefore || hasLimit) {
		httpresp.Error(w, http.StatusBadRequest, "since/until cannot be combined with limit/before")
		return
	}

	if hasSince || hasUntil {
		since, err := parseOptionalTimeParam(r, "since")
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		until, err := parseOptionalTimeParam(r, "until")
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "until must be an RFC3339 timestamp")
			return
		}
		sessions, err := h.repo.ListInRange(r.Context(), userID, since, until)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list running sessions in range", err)
			return
		}
		out := make([]sessionDTO, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, toSessionDTO(s, false))
		}
		httpresp.OK(w, "listed running sessions", listResponse{Sessions: out, NextBefore: nil})
		return
	}

	limit := listLimitDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpresp.Error(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
		if limit > listLimitMax {
			limit = listLimitMax
		}
	}

	var before *time.Time
	if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "before must be an RFC3339 timestamp")
			return
		}
		before = &t
	}

	sessions, err := h.repo.List(r.Context(), userID, limit, before)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list running sessions", err)
		return
	}

	out := make([]sessionDTO, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toSessionDTO(s, false))
	}

	// next_before is the cursor for the next page: the start_time of the
	// last returned session, but only when we filled the page (more rows
	// may exist). A short page means we reached the end → null cursor.
	var nextBefore *string
	if len(sessions) == limit && len(sessions) > 0 {
		cursor := sessions[len(sessions)-1].StartTime.Format(time.RFC3339)
		nextBefore = &cursor
	}

	httpresp.OK(w, "listed running sessions", listResponse{Sessions: out, NextBefore: nextBefore})
}

// parseOptionalTimeParam returns nil when the param is absent or empty,
// the parsed RFC3339 time when present, or an error when present but
// malformed. Keeps the listSessions branches readable.
func parseOptionalTimeParam(r *http.Request, name string) (*time.Time, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "session id is required")
		return
	}
	s, err := h.repo.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "running session not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get running session", err)
		return
	}
	httpresp.OK(w, "fetched running session", toSessionDTO(*s, true))
}

func (h *Handler) renameSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "session id is required")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpresp.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(name) > 200 {
		httpresp.Error(w, http.StatusBadRequest, "name is too long")
		return
	}
	s, err := h.repo.Rename(r.Context(), userID, id, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "running session not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "rename running session", err)
		return
	}
	httpresp.OK(w, "renamed running session", toSessionDTO(*s, false))
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "session id is required")
		return
	}
	if err := h.repo.SoftDelete(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "running session not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete running session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	tzName := r.URL.Query().Get("timezone")
	if tzName == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid timezone "+tzName)
		return
	}

	now := time.Now().UTC()
	m, err := h.repo.Metrics(r.Context(), userID, now, loc)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "running metrics", err)
		return
	}

	resp := metricsResponse{
		CurrentWeek: currentWeekDTO{
			DistanceMeters:      m.CurrentWeek.DistanceMeters,
			RunCount:            m.CurrentWeek.RunCount,
			DeltaPctVsPriorWeek: m.DeltaPctVsPriorWeek,
		},
		CurrentMonth: periodStatDTO{
			DistanceMeters: m.CurrentMonth.DistanceMeters,
			RunCount:       m.CurrentMonth.RunCount,
		},
		RecentAvgPaceSecPerKm: m.RecentAvgPaceSecPerKm,
		AllTime: periodStatDTO{
			DistanceMeters: m.AllTime.DistanceMeters,
			RunCount:       m.AllTime.RunCount,
		},
	}
	httpresp.OK(w, "running metrics", resp)
}
