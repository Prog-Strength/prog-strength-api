package activity

import (
	"encoding/json"
	"errors"
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

// maxTCXBytes caps the multipart upload size. A typical activity TCX is
// a few hundred KB; 10 MB is generous headroom while bounding memory per
// import (the whole file is read into a byte slice for parse + archive).
const maxTCXBytes = 10 << 20

// listLimitDefault / listLimitMax bound the page size for GET /activities.
const (
	listLimitDefault = 50
	listLimitMax     = 100
)

// Handler exposes the HTTP surface for activities: TCX import, the
// list/detail/rename/delete CRUD, and the running-specific dashboard
// metrics tiles.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// Mount registers routes under /activities. Callers are expected to
// have already wrapped the router in auth.RequireUser — these handlers
// read the user ID from request context and assume it's present.
//
// POST /activities/tcx is the manual-upload ingest path. A future
// Garmin Connect sync wires into IngestTCX via a different transport
// (likely a background worker triggered by a webhook); it doesn't need
// a new HTTP route.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/activities", func(r chi.Router) {
		r.Post("/tcx", h.uploadTCX)
		r.Get("/", h.list)
		r.Get("/running-metrics", h.runningMetrics)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.rename)
		r.Delete("/{id}", h.delete)
	})

	// Running best efforts (running PRs). The data lives in the activity
	// repository, so the handler that already owns it hosts the /running
	// surface rather than spinning up a separate package.
	r.Route("/running", func(r chi.Router) {
		r.Get("/best-efforts", h.runningBestEfforts)
		r.Get("/best-efforts/{distance_key}/history", h.runningBestEffortHistory)
	})
}

// standardDistanceByKey returns the StandardDistance for a key and whether
// it's a known standard distance. The StandardDistances slice is the single
// source of truth shared with the summarizer, repository, and DB CHECK.
func standardDistanceByKey(key string) (StandardDistance, bool) {
	for _, d := range StandardDistances {
		if d.Key == key {
			return d, true
		}
	}
	return StandardDistance{}, false
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

// activityDTO is the wire shape of an activity. Nullable numerics are
// pointers WITHOUT omitempty so the key is always present (rendered null
// when nil) — the client relies on a stable key set. Trackpoints is the
// one exception: omitempty drops it on list responses, which never load
// the per-point stream.
type activityDTO struct {
	ID                  string          `json:"id"`
	ActivityType        ActivityType    `json:"activity_type"`
	IngestSource        IngestSource    `json:"ingest_source"`
	SourceActivityID    string          `json:"source_activity_id"`
	Name                *string         `json:"name"`
	StartTime           time.Time       `json:"start_time"`
	DistanceMeters      float64         `json:"distance_meters"`
	DurationSeconds     int             `json:"duration_seconds"`
	AvgPaceSecPerKm     *float64        `json:"avg_pace_sec_per_km"`
	BestPaceSecPerKm    *float64        `json:"best_pace_sec_per_km"`
	AvgHeartRateBpm     *int            `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm     *int            `json:"max_heart_rate_bpm"`
	TotalCalories       *int            `json:"total_calories"`
	ElevationGainMeters *float64        `json:"elevation_gain_meters"`
	CreatedAt           time.Time       `json:"created_at"`
	Trackpoints         []trackpointDTO `json:"trackpoints,omitempty"`
}

func toActivityDTO(a Activity, withTrackpoints bool) activityDTO {
	dto := activityDTO{
		ID:                  a.ID,
		ActivityType:        a.ActivityType,
		IngestSource:        a.IngestSource,
		SourceActivityID:    a.SourceActivityID,
		Name:                a.Name,
		StartTime:           a.StartTime,
		DistanceMeters:      a.DistanceMeters,
		DurationSeconds:     a.DurationSeconds,
		AvgPaceSecPerKm:     a.AvgPaceSecPerKm,
		BestPaceSecPerKm:    a.BestPaceSecPerKm,
		AvgHeartRateBpm:     a.AvgHeartRateBpm,
		MaxHeartRateBpm:     a.MaxHeartRateBpm,
		TotalCalories:       a.TotalCalories,
		ElevationGainMeters: a.ElevationGainMeters,
		CreatedAt:           a.CreatedAt,
	}
	if withTrackpoints {
		dto.Trackpoints = make([]trackpointDTO, 0, len(a.Trackpoints))
		for _, tp := range a.Trackpoints {
			dto.Trackpoints = append(dto.Trackpoints, trackpointDTO(tp))
		}
	}
	return dto
}

// listResponse is the GET /activities payload: a page of activities
// plus the keyset cursor for the next page. NextBefore is null when this
// is the last page (fewer than limit returned).
type listResponse struct {
	Activities []activityDTO `json:"activities"`
	NextBefore *string       `json:"next_before"`
}

// metricsResponse and its sub-shapes mirror the SOW JSON for the running
// dashboard tiles. Nullable rollup fields stay present-as-null (no
// omitempty).
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

// uploadTCX is the manual-upload entry point. It accepts a multipart
// form with a "file" field carrying a Garmin TCX export, runs the
// shared IngestTCX seam with source=ManualTCX, and returns the created
// activity as JSON. Failure modes carry machine-readable codes (see
// ErrorWithCode) so the import client can show a precise reason.
func (h *Handler) uploadTCX(w http.ResponseWriter, r *http.Request) {
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
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return
		}
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "expected a multipart upload with a file field", "unsupported_media_type")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "missing file field in multipart upload", "unsupported_media_type")
		return
	}
	defer file.Close()

	rid := requestid.FromContext(r.Context())
	a, err := IngestTCX(r.Context(), h.repo, userID, IngestManualTCX, file)
	switch {
	case err == nil:
		log.Printf("activity import: request_id=%s user_id=%s source=%s source_activity_id=%s activity_type=%s outcome=imported",
			rid, userID, a.IngestSource, a.SourceActivityID, a.ActivityType)
		httpresp.Created(w, "imported activity", toActivityDTO(a, true))
	case errors.Is(err, ErrDuplicate):
		// IngestTCX returns the existing live row alongside ErrDuplicate
		// when the lookup succeeds; a.ID is empty if it didn't (rare —
		// race with a concurrent delete). Surface what we have.
		log.Printf("activity import: request_id=%s user_id=%s existing_activity_id=%s outcome=duplicate", rid, userID, a.ID)
		httpresp.ErrorWithCodeData(w, http.StatusConflict, "an activity for this source upload already exists", "duplicate_activity", map[string]any{
			"existing_activity_id": a.ID,
		})
	case errors.Is(err, ErrStorage):
		log.Printf("activity import: request_id=%s user_id=%s outcome=storage_failed err=%v", rid, userID, err)
		httpresp.ErrorWithCode(w, http.StatusInternalServerError, "failed to archive tcx file", "storage_failed")
	default:
		var verr *ValidationError
		if errors.As(err, &verr) {
			log.Printf("activity import: request_id=%s user_id=%s outcome=invalid slug=%s", rid, userID, verr.Slug)
			httpresp.ErrorWithCode(w, http.StatusBadRequest, verr.Msg, verr.Slug)
			return
		}
		// MaxBytesReader can also fire from inside IngestTCX's io.ReadAll.
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return
		}
		httpresp.ServerError(w, r.Context(), "ingest tcx", err)
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

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
		activities, err := h.repo.ListInRange(r.Context(), userID, since, until)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list activities in range", err)
			return
		}
		out := make([]activityDTO, 0, len(activities))
		for _, a := range activities {
			out = append(out, toActivityDTO(a, false))
		}
		httpresp.OK(w, "listed activities", listResponse{Activities: out, NextBefore: nil})
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

	activities, err := h.repo.List(r.Context(), userID, limit, before)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list activities", err)
		return
	}

	out := make([]activityDTO, 0, len(activities))
	for _, a := range activities {
		out = append(out, toActivityDTO(a, false))
	}

	var nextBefore *string
	if len(activities) == limit && len(activities) > 0 {
		cursor := activities[len(activities)-1].StartTime.Format(time.RFC3339)
		nextBefore = &cursor
	}

	httpresp.OK(w, "listed activities", listResponse{Activities: out, NextBefore: nextBefore})
}

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

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	activityID := chi.URLParam(r, "id")
	if activityID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id is required")
		return
	}
	a, err := h.repo.Get(r.Context(), userID, activityID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get activity", err)
		return
	}
	httpresp.OK(w, "fetched activity", toActivityDTO(*a, true))
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	activityID := chi.URLParam(r, "id")
	if activityID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id is required")
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
	a, err := h.repo.Rename(r.Context(), userID, activityID, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "rename activity", err)
		return
	}
	httpresp.OK(w, "renamed activity", toActivityDTO(*a, false))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	activityID := chi.URLParam(r, "id")
	if activityID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id is required")
		return
	}
	if err := h.repo.SoftDelete(r.Context(), userID, activityID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete activity", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) runningMetrics(w http.ResponseWriter, r *http.Request) {
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
	m, err := h.repo.RunningMetrics(r.Context(), userID, now, loc)
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

// --- Running best efforts -----------------------------------------------

// bestEffortDTO is one entry in the GET /running/best-efforts response:
// the user's current best at a standard distance plus the activity that
// set it. pace_sec_per_km is derived at request time (not stored).
type bestEffortDTO struct {
	DistanceKey       string    `json:"distance_key"`
	DistanceLabel     string    `json:"distance_label"`
	DistanceMeters    float64   `json:"distance_meters"`
	DurationSeconds   float64   `json:"duration_seconds"`
	PaceSecPerKm      float64   `json:"pace_sec_per_km"`
	ActivityID        string    `json:"activity_id"`
	ActivityStartTime time.Time `json:"activity_start_time"`
}

// bestEffortsResponse wraps the bests list. best_efforts is always a
// non-nil slice so it serializes as [] (not null) for users with no runs.
type bestEffortsResponse struct {
	BestEfforts []bestEffortDTO `json:"best_efforts"`
}

// bestEffortHistoryPointDTO is one point in a single distance's
// progression series (every activity that achieved a best effort at the
// distance).
type bestEffortHistoryPointDTO struct {
	ActivityID        string    `json:"activity_id"`
	ActivityStartTime time.Time `json:"activity_start_time"`
	DurationSeconds   float64   `json:"duration_seconds"`
}

// bestEffortHistoryResponse is the GET
// /running/best-efforts/{distance_key}/history payload.
type bestEffortHistoryResponse struct {
	DistanceKey    string                      `json:"distance_key"`
	DistanceLabel  string                      `json:"distance_label"`
	DistanceMeters float64                     `json:"distance_meters"`
	Points         []bestEffortHistoryPointDTO `json:"points"`
}

// runningBestEfforts handles GET /running/best-efforts: the user's current
// best across each standard distance, sorted in StandardDistances order.
// Distances never covered are omitted; pace is derived from duration and
// the distance's meters.
func (h *Handler) runningBestEfforts(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	efforts, err := h.repo.GetUserRunningBestEfforts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best efforts", err)
		return
	}

	// Index the repo rows by distance_key, then emit in StandardDistances
	// order so the response is consistently sorted shortest-first without
	// the client re-sorting.
	byKey := make(map[string]RunningBestEffort, len(efforts))
	for _, e := range efforts {
		byKey[e.DistanceKey] = e
	}

	out := make([]bestEffortDTO, 0, len(efforts))
	for _, d := range StandardDistances {
		e, ok := byKey[d.Key]
		if !ok {
			continue
		}
		out = append(out, bestEffortDTO{
			DistanceKey:       d.Key,
			DistanceLabel:     d.DisplayName,
			DistanceMeters:    d.Meters,
			DurationSeconds:   e.DurationSeconds,
			PaceSecPerKm:      e.DurationSeconds / (d.Meters / 1000),
			ActivityID:        e.ActivityID,
			ActivityStartTime: e.ActivityStartTime,
		})
	}

	httpresp.OK(w, "listed running best efforts", bestEffortsResponse{BestEfforts: out})
}

// runningBestEffortHistory handles GET
// /running/best-efforts/{distance_key}/history: every activity that
// achieved a best effort at the distance, ascending by start time. An
// unknown distance_key is a 404 with code unknown_distance_key.
func (h *Handler) runningBestEffortHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	distanceKey := chi.URLParam(r, "distance_key")
	d, ok := standardDistanceByKey(distanceKey)
	if !ok {
		httpresp.ErrorWithCode(w, http.StatusNotFound, "unknown distance key", "unknown_distance_key")
		return
	}

	points, err := h.repo.GetRunningBestEffortHistory(r.Context(), userID, distanceKey)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best effort history", err)
		return
	}

	pts := make([]bestEffortHistoryPointDTO, 0, len(points))
	for _, p := range points {
		pts = append(pts, bestEffortHistoryPointDTO{
			ActivityID:        p.ActivityID,
			ActivityStartTime: p.ActivityStartTime,
			DurationSeconds:   p.DurationSeconds,
		})
	}

	httpresp.OK(w, "listed running best effort history", bestEffortHistoryResponse{
		DistanceKey:    d.Key,
		DistanceLabel:  d.DisplayName,
		DistanceMeters: d.Meters,
		Points:         pts,
	})
}
