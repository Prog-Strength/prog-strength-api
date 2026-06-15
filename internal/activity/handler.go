package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/running/estimate"
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
	// now supplies the current time; defaulted to time.Now and overridable
	// in tests for deterministic recency weighting and back-test dates.
	now func() time.Time
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo, now: time.Now} }

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
		r.Get("/max-effort", h.runningMaxEffort)
		r.Get("/max-effort/{distance_key}", h.runningMaxEffortDetail)
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

// --- Running max-effort estimates ---------------------------------------

// assembleAttempts flattens the user's best-effort history across every
// standard distance into the engine's Attempt shape. The estimator needs
// efforts at ALL distances (not just the target) because the curve fit
// draws its power from multi-distance evidence.
func (h *Handler) assembleAttempts(ctx context.Context, userID string) ([]estimate.Attempt, error) {
	var attempts []estimate.Attempt
	for _, d := range StandardDistances {
		points, err := h.repo.GetRunningBestEffortHistory(ctx, userID, d.Key)
		if err != nil {
			return nil, err
		}
		for _, p := range points {
			attempts = append(attempts, estimate.Attempt{
				DistanceKey:            d.Key,
				DistanceMeters:         d.Meters,
				DurationSeconds:        p.DurationSeconds,
				AchievedAt:             p.ActivityStartTime,
				ActivityDistanceMeters: p.ActivityDistanceMeters,
			})
		}
	}
	return attempts, nil
}

// maxEffortSummaryEntryDTO is one distance row in the cross-distance
// summary. Estimate-derived numeric fields and basis/confidence are
// pointers so they render null when the engine returns insufficient_data;
// actual_best_* are likewise nullable and present only when the user has a
// best at that distance.
type maxEffortSummaryEntryDTO struct {
	DistanceKey    string  `json:"distance_key"`
	DistanceLabel  string  `json:"distance_label"`
	DistanceMeters float64 `json:"distance_meters"`

	EstimateSeconds *float64 `json:"estimate_seconds"`
	LowerSeconds    *float64 `json:"lower_seconds"`
	UpperSeconds    *float64 `json:"upper_seconds"`
	Basis           *string  `json:"basis"`
	Confidence      *string  `json:"confidence"`

	ActualBestSeconds    *float64   `json:"actual_best_seconds"`
	ActualBestActivityID *string    `json:"actual_best_activity_id"`
	ActualBestAchievedAt *time.Time `json:"actual_best_achieved_at"`
}

// maxEffortSummaryResponse is the GET /running/max-effort payload.
type maxEffortSummaryResponse struct {
	EstimatorVersion string                     `json:"estimator_version"`
	Distances        []maxEffortSummaryEntryDTO `json:"distances"`
}

// runningMaxEffort handles GET /running/max-effort: a cross-distance
// summary of the user's predicted race time at each standard distance,
// alongside their actual best where they have one.
func (h *Handler) runningMaxEffort(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	attempts, err := h.assembleAttempts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "assemble attempts", err)
		return
	}
	bests, err := h.repo.GetUserRunningBestEfforts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best efforts", err)
		return
	}
	bestByKey := make(map[string]RunningBestEffort, len(bests))
	for _, b := range bests {
		bestByKey[b.DistanceKey] = b
	}

	est := estimate.NewEstimator()
	now := h.now().UTC()

	distances := make([]maxEffortSummaryEntryDTO, 0, len(StandardDistances))
	for _, d := range StandardDistances {
		res := est.Estimate(estimate.EstimateInput{
			TargetDistanceKey:    d.Key,
			TargetDistanceMeters: d.Meters,
			Attempts:             attempts,
			Now:                  now,
		})

		entry := maxEffortSummaryEntryDTO{
			DistanceKey:    d.Key,
			DistanceLabel:  d.DisplayName,
			DistanceMeters: d.Meters,
		}
		if res.Basis != "insufficient_data" {
			seconds := res.Seconds
			lower := res.LowerSeconds
			upper := res.UpperSeconds
			basis := res.Basis
			confidence := res.Confidence
			entry.EstimateSeconds = &seconds
			entry.LowerSeconds = &lower
			entry.UpperSeconds = &upper
			entry.Basis = &basis
			entry.Confidence = &confidence
		}
		if b, ok := bestByKey[d.Key]; ok {
			seconds := b.DurationSeconds
			activityID := b.ActivityID
			achievedAt := b.ActivityStartTime
			entry.ActualBestSeconds = &seconds
			entry.ActualBestActivityID = &activityID
			entry.ActualBestAchievedAt = &achievedAt
		}
		distances = append(distances, entry)
	}

	httpresp.OK(w, "running max-effort estimates", maxEffortSummaryResponse{
		EstimatorVersion: estimate.EstimatorVersion,
		Distances:        distances,
	})
}

// --- Running max-effort detail ------------------------------------------

// maxEffortHistoryPointDTO is one back-tested estimate, computed on read by
// re-running the engine against the efforts known as of an earlier date.
type maxEffortHistoryPointDTO struct {
	AsOf         string  `json:"as_of"`
	Seconds      float64 `json:"seconds"`
	LowerSeconds float64 `json:"lower_seconds"`
	UpperSeconds float64 `json:"upper_seconds"`
}

// maxEffortAttemptDTO is one effort at the target distance: the raw data
// point with a derived pace and the quality classification used by the
// estimator's weight.
type maxEffortAttemptDTO struct {
	ActivityID      string  `json:"activity_id"`
	AchievedAt      string  `json:"achieved_at"`
	DurationSeconds float64 `json:"duration_seconds"`
	PaceSecPerKm    float64 `json:"pace_sec_per_km"`
	Source          string  `json:"source"`
}

// maxEffortActualBestDTO is the user's actual best at the target distance.
type maxEffortActualBestDTO struct {
	Seconds    float64 `json:"seconds"`
	ActivityID string  `json:"activity_id"`
	AchievedAt string  `json:"achieved_at"`
}

// maxEffortEstimateDTO is the current prediction block, null when the
// engine has insufficient data.
type maxEffortEstimateDTO struct {
	Seconds      float64 `json:"seconds"`
	LowerSeconds float64 `json:"lower_seconds"`
	UpperSeconds float64 `json:"upper_seconds"`
	Basis        string  `json:"basis"`
	Confidence   string  `json:"confidence"`
	NPoints      int     `json:"n_points"`
	NDistances   int     `json:"n_distances"`
}

// maxEffortStatsDTO is the summary tile: estimate vs. current best and a
// human data summary. Nullable numerics are pointers (present-as-null).
type maxEffortStatsDTO struct {
	EstimatedMaxEffortSeconds *float64 `json:"estimated_max_effort_seconds"`
	CurrentBestSeconds        *float64 `json:"current_best_seconds"`
	GapSeconds                *float64 `json:"gap_seconds"`
	Confidence                string   `json:"confidence"`
	DataSummary               string   `json:"data_summary"`
}

// maxEffortDetailResponse is the GET /running/max-effort/{distance_key}
// payload.
type maxEffortDetailResponse struct {
	DistanceKey      string                     `json:"distance_key"`
	DistanceLabel    string                     `json:"distance_label"`
	DistanceMeters   float64                    `json:"distance_meters"`
	EstimatorVersion string                     `json:"estimator_version"`
	Estimate         *maxEffortEstimateDTO      `json:"estimate"`
	EstimateHistory  []maxEffortHistoryPointDTO `json:"estimate_history"`
	Attempts         []maxEffortAttemptDTO      `json:"attempts"`
	ActualBest       *maxEffortActualBestDTO    `json:"actual_best"`
	Stats            maxEffortStatsDTO          `json:"stats"`
}

// runningMaxEffortDetail handles GET /running/max-effort/{distance_key}:
// the current estimate at one distance plus an on-read back-test history,
// the contributing attempts, and the user's actual best. An unknown
// distance_key is a 404 with code unknown_distance_key. insufficient_data
// is a 200 with estimate null and an explanatory basis in stats.
func (h *Handler) runningMaxEffortDetail(w http.ResponseWriter, r *http.Request) {
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

	attempts, err := h.assembleAttempts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "assemble attempts", err)
		return
	}

	est := estimate.NewEstimator()
	now := h.now().UTC()

	res := est.Estimate(estimate.EstimateInput{
		TargetDistanceKey:    d.Key,
		TargetDistanceMeters: d.Meters,
		Attempts:             attempts,
		Now:                  now,
	})

	// Target-distance efforts drive the attempts list and the back-test
	// dates. They're already ascending by achieved-at from assembleAttempts'
	// per-distance history query.
	var targetAttempts []estimate.Attempt
	for _, a := range attempts {
		if a.DistanceKey == d.Key {
			targetAttempts = append(targetAttempts, a)
		}
	}

	// estimate_history: re-run the engine at each distinct target-distance
	// effort date, seeing only efforts known by then, so the chart shows how
	// the prediction evolved. Use end-of-day as Now so all efforts on a date
	// are visible to that date's estimate.
	history := make([]maxEffortHistoryPointDTO, 0)
	seenDate := map[string]struct{}{}
	var dates []time.Time
	for _, a := range targetAttempts {
		day := a.AchievedAt.Format("2006-01-02")
		if _, ok := seenDate[day]; ok {
			continue
		}
		seenDate[day] = struct{}{}
		y, m, dd := a.AchievedAt.Date()
		eod := time.Date(y, m, dd, 23, 59, 59, 0, a.AchievedAt.Location())
		dates = append(dates, eod)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	for _, t := range dates {
		var filtered []estimate.Attempt
		for _, a := range attempts {
			if !a.AchievedAt.After(t) {
				filtered = append(filtered, a)
			}
		}
		hr := est.Estimate(estimate.EstimateInput{
			TargetDistanceKey:    d.Key,
			TargetDistanceMeters: d.Meters,
			Attempts:             filtered,
			Now:                  t,
		})
		if hr.Basis == "insufficient_data" {
			continue
		}
		history = append(history, maxEffortHistoryPointDTO{
			AsOf:         t.Format("2006-01-02"),
			Seconds:      hr.Seconds,
			LowerSeconds: hr.LowerSeconds,
			UpperSeconds: hr.UpperSeconds,
		})
	}

	// attempts (this distance only), ascending by achieved-at.
	attemptDTOs := make([]maxEffortAttemptDTO, 0, len(targetAttempts))
	for _, a := range targetAttempts {
		attemptDTOs = append(attemptDTOs, maxEffortAttemptDTO{
			ActivityID:      "",
			AchievedAt:      a.AchievedAt.Format(time.RFC3339),
			DurationSeconds: a.DurationSeconds,
			PaceSecPerKm:    a.DurationSeconds / (d.Meters / 1000),
			Source:          estimate.ClassifySource(d.Meters, a.ActivityDistanceMeters),
		})
	}
	// Attempt doesn't carry the activity id, so re-read the target history
	// for ids in the same ascending order.
	targetPoints, err := h.repo.GetRunningBestEffortHistory(r.Context(), userID, d.Key)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best effort history", err)
		return
	}
	for i := range attemptDTOs {
		if i < len(targetPoints) {
			attemptDTOs[i].ActivityID = targetPoints[i].ActivityID
		}
	}

	// actual_best at this distance.
	bests, err := h.repo.GetUserRunningBestEfforts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best efforts", err)
		return
	}
	var actualBest *maxEffortActualBestDTO
	var currentBestSeconds *float64
	for _, b := range bests {
		if b.DistanceKey == d.Key {
			actualBest = &maxEffortActualBestDTO{
				Seconds:    b.DurationSeconds,
				ActivityID: b.ActivityID,
				AchievedAt: b.ActivityStartTime.Format(time.RFC3339),
			}
			s := b.DurationSeconds
			currentBestSeconds = &s
			break
		}
	}

	// estimate block + stats.
	var estimateBlock *maxEffortEstimateDTO
	var estimatedSeconds *float64
	stats := maxEffortStatsDTO{
		CurrentBestSeconds: currentBestSeconds,
		DataSummary:        fmt.Sprintf("%d efforts across %d distances", res.NPoints, res.NDistances),
	}
	if res.Basis != "insufficient_data" {
		estimateBlock = &maxEffortEstimateDTO{
			Seconds:      res.Seconds,
			LowerSeconds: res.LowerSeconds,
			UpperSeconds: res.UpperSeconds,
			Basis:        res.Basis,
			Confidence:   res.Confidence,
			NPoints:      res.NPoints,
			NDistances:   res.NDistances,
		}
		s := res.Seconds
		estimatedSeconds = &s
		stats.EstimatedMaxEffortSeconds = &s
		stats.Confidence = res.Confidence
	} else {
		stats.Confidence = res.Basis
	}
	if estimatedSeconds != nil && currentBestSeconds != nil {
		gap := *estimatedSeconds - *currentBestSeconds
		stats.GapSeconds = &gap
	}

	httpresp.OK(w, "running max-effort estimate", maxEffortDetailResponse{
		DistanceKey:      d.Key,
		DistanceLabel:    d.DisplayName,
		DistanceMeters:   d.Meters,
		EstimatorVersion: estimate.EstimatorVersion,
		Estimate:         estimateBlock,
		EstimateHistory:  history,
		Attempts:         attemptDTOs,
		ActualBest:       actualBest,
		Stats:            stats,
	})
}
