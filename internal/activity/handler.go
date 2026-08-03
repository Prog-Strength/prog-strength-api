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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/running/estimate"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
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

// maxNotesLength caps a session's free-text note. Long enough for a real
// post-session reflection, short enough that one note is a single cheap
// distillation unit for the memory pipeline. The web/mobile editors cap
// their inputs at the same number.
const maxNotesLength = 2000

// Calibration rejects a correction factor outside these bounds to catch a
// unit-entry mistake (miles typed as meters, etc.). Documented in the SOW;
// tune if a real calibration legitimately exceeds them.
const (
	calibrationMinFactor = 0.5
	calibrationMaxFactor = 2.0
)

// Handler exposes the HTTP surface for activities: TCX import, the
// list/detail/rename/delete CRUD, and the running-specific dashboard
// metrics tiles.
type Handler struct {
	repo Repository
	// now supplies the current time; defaulted to time.Now and overridable
	// in tests for deterministic recency weighting and back-test dates.
	now func() time.Time
	// publisher pushes newly-ingested runs and their best efforts into the
	// timeline feed index. Optional and nil-safe: existing constructions
	// (incl. tests) leave it nil and skip publishing. Injected
	// post-construction via SetPublisher so NewHandler's signature — and the
	// tests that call it — stay untouched.
	publisher timeline.Publisher
	// planMatcher best-effort links a freshly-ingested run (or a deleted one)
	// to the planned workout it completes. Optional and nil-safe, mirroring
	// publisher: existing constructions leave it nil and skip matching. The
	// implementation lives in server wiring so this package never imports
	// planned_workout. Injected post-construction via SetPlanMatcher.
	planMatcher PlanMatcher
	// hrEngine computes the percent-of-max-HR zone breakdown attached to a
	// running activity's detail response. Optional and nil-safe, mirroring
	// publisher/planMatcher: when nil the get handler skips the zones block
	// entirely. Injected post-construction via SetHRZonesEngine.
	hrEngine *hrzones.Engine
	// hrWindow is the recency window over which RecentHRStats summarizes the
	// user's HR history for the reference-max-HR estimate. Set alongside
	// hrEngine; derived from the [hr_zones] recency_window_days config.
	hrWindow time.Duration
	// demographicsLoader supplies profile fields for max-effort estimation.
	// Optional and nil-safe.
	demographicsLoader DemographicsLoader
	// registry resolves per-type behavior (create validation, detail
	// stores, card summaries) for the unified /activities surface. Injected
	// post-construction via SetRegistry from server wiring; no route reads
	// it yet — the unified surface layers on next.
	registry *Registry
	// photoStore / photoRepo / photoCfg back the activity-photo write path
	// (POST/PATCH/PUT/DELETE under /activities/{id}/photos). Optional and
	// nil-safe: when either photoStore or photoRepo is nil the endpoints
	// return 503. Injected post-construction via SetPhotoStore from server
	// wiring, which translates config.PhotosConfig into the local
	// PhotosConfig below so this package never imports internal/config.
	photoStore PhotoStore
	photoRepo  PhotoRepository
	photoCfg   PhotosConfig

	// videoStore / videoRepo / videoCfg back the activity-video path. Same
	// nil-safe contract as photos: when either is nil the video endpoints
	// return 503 and the detail read simply omits the videos array, which is
	// what lets api and infra ship in either order.
	videoStore VideoStore
	videoRepo  VideoRepository
	videoCfg   VideosConfig
}

// PhotosConfig mirrors config.PhotosConfig for the activity photo write path.
// It is declared here (rather than importing internal/config) so the activity
// package stays free of a config dependency; server wiring copies the fields
// across when it calls SetPhotoStore.
type PhotosConfig struct {
	MaxPerActivity     int
	MaxUploadBytes     int64
	FullMaxEdgePx      int
	FullJPEGQuality    int
	ThumbMaxEdgePx     int
	ThumbJPEGQuality   int
	PresignWindowHours int
	CaptionMaxChars    int
	// UploadURLTTLMinutes bounds the presigned PUT the client uploads to.
	// Long enough for a large photo on a slow connection, short enough that
	// an abandoned reservation is reaped promptly.
	UploadURLTTLMinutes int
	// ProcessMaxAttempts caps how many times the worker will pick up one
	// photo before giving up on it.
	ProcessMaxAttempts int
	// ProcessTickSeconds is the worker's poll interval.
	ProcessTickSeconds int
	// ReapAfterMinutes is how long a reservation may sit unused before the
	// reaper retires it and deletes any staged object.
	ReapAfterMinutes int
}

// VideosConfig mirrors config.VideosConfig for the activity video path, for the
// same reason PhotosConfig does: the activity package stays free of a config
// dependency and server wiring copies the fields across.
type VideosConfig struct {
	MaxPerActivity        int
	MaxUploadBytes        int64
	AllowedContentTypes   []string
	PresignWindowHours    int
	UploadURLTTLMinutes   int
	PosterMaxEdgePx       int
	PosterJPEGQuality     int
	CaptionMaxChars       int
	PendingReapAfterHours int
}

// allowsContentType reports whether ct is in the configured container
// allowlist. An empty allowlist denies everything — fail closed, so a
// misconfigured deploy refuses uploads rather than accepting anything.
func (c VideosConfig) allowsContentType(ct string) bool {
	for _, allowed := range c.AllowedContentTypes {
		if allowed == ct {
			return true
		}
	}
	return false
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo, now: time.Now} }

// SetPublisher wires the timeline publisher in so an ingested run (and its
// best efforts) appears in the feed. Called from server wiring after
// construction. Safe to never call — publishing is best-effort and
// nil-guarded.
func (h *Handler) SetPublisher(p timeline.Publisher) { h.publisher = p }

// SetPlanMatcher wires the plan matcher in so an ingested run is best-effort
// linked to the planned workout it completes (and unlinked on delete). Called
// from server wiring after construction. Safe to never call — matching is
// best-effort and nil-guarded.
func (h *Handler) SetPlanMatcher(m PlanMatcher) { h.planMatcher = m }

// SetHRZonesEngine wires the heart-rate-zone engine (and its recency window)
// in so an activity's detail response carries a heart_rate_zones block whenever
// it has per-point HR — any type, not just running. Called from server wiring
// after construction. Safe to never call — the get handler nil-guards and
// simply omits the block when the engine is unset.
func (h *Handler) SetHRZonesEngine(e *hrzones.Engine, window time.Duration) {
	h.hrEngine = e
	h.hrWindow = window
}

// SetDemographicsLoader wires profile fields into max-effort estimation.
func (h *Handler) SetDemographicsLoader(l DemographicsLoader) {
	h.demographicsLoader = l
}

// SetRegistry wires the activity type registry in. Called from server wiring
// after construction, mirroring the other setters so NewHandler's signature
// (and the tests that call it) stay untouched. Nothing consumes it yet.
func (h *Handler) SetRegistry(reg *Registry) { h.registry = reg }

// SetPhotoStore wires the object store, photo repository, and photo tunables in
// so the activity-photo write endpoints work. Called from server wiring after
// construction. Safe to never call — the endpoints nil-guard photoStore/photoRepo
// and return 503 when either is unset.
func (h *Handler) SetPhotoStore(store PhotoStore, repo PhotoRepository, cfg PhotosConfig) {
	h.photoStore = store
	h.photoRepo = repo
	h.photoCfg = cfg
}

// SetVideoStore wires the object store, video repository, and video tunables in
// so the activity-video endpoints work. Called from server wiring after
// construction. Safe to never call — every endpoint nil-guards and returns 503,
// and the detail read omits the videos array entirely.
func (h *Handler) SetVideoStore(store VideoStore, repo VideoRepository, cfg VideosConfig) {
	h.videoStore = store
	h.videoRepo = repo
	h.videoCfg = cfg
}

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
// logs + meters any EnsurePost error and swallows it. The backfill repairs
// any gap, so a feed-index hiccup must never fail an activity import.
func (h *Handler) publish(ctx context.Context, ref timeline.PostRef) {
	if h.publisher == nil {
		return
	}
	_ = h.publisher.EnsurePost(ctx, ref)
}

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
		// /tcx stays on the base handler: the upload is cross-type endurance
		// ingest (the TCX <Sport> tag decides running/walking/cycling), so no
		// single descriptor owns it.
		r.Post("/tcx", h.uploadTCX)
		r.Get("/", h.list)
		r.Get("/running-metrics", h.runningMetrics)
		r.Post("/", h.createUnified)
		r.Get("/{id}", h.get)
		// PATCH stays on the base handler too: rename is genuinely
		// cross-type, and the environment toggle shares the route.
		r.Patch("/{id}", h.patch)
		r.Put("/{id}", h.updateUnified)
		r.Delete("/{id}", h.delete)
		// Activity photos (write path). Reads are served elsewhere; these
		// mutate the photo set for one activity. Nil-guarded in the handlers.
		r.Post("/{id}/photos", h.uploadPhoto)
		// Two-phase upload. The synchronous route above stays mounted until
		// the web client has moved; see the SOW's sequencing.
		r.Post("/{id}/photos/reserve", h.reservePhoto)
		r.Post("/{id}/photos/{photo_id}/commit", h.commitPhoto)
		r.Patch("/{id}/photos/{photo_id}", h.patchPhotoCaption)
		r.Put("/{id}/photos/order", h.reorderPhotos)
		r.Delete("/{id}/photos/{photo_id}", h.deletePhoto)
		// Activity videos. Two-phase upload: reserve mints a presigned PUT the
		// client uploads directly to S3, complete confirms the object and
		// attaches the poster. Video bytes never pass through this process.
		// Nil-guarded in the handlers, same as photos.
		//
		// The /order route is registered BEFORE /{video_id} so chi doesn't
		// route a PUT of "order" into the caption/param handler.
		r.Post("/{id}/videos", h.reserveVideo)
		r.Post("/{id}/videos/{video_id}/complete", h.completeVideo)
		r.Put("/{id}/videos/order", h.reorderVideos)
		r.Patch("/{id}/videos/{video_id}", h.patchVideoCaption)
		r.Delete("/{id}/videos/{video_id}", h.deleteVideo)
		// Type-specific routes (running calibrate, strength TCX enrichment
		// and progression) mount through the registered descriptors, wired
		// in server.go.
		if h.registry != nil {
			for _, d := range h.registry.Descriptors() {
				if d.MountRoutes != nil {
					d.MountRoutes(r)
				}
			}
		}
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

// MountRunningRoutes is the running descriptor's MountRoutes: the routes
// only a run can answer, mounted on the /activities subrouter. Assigned to
// the descriptor in server.go (same package as the handler, so the method
// can stay bound to the unexported handler internals).
func (h *Handler) MountRunningRoutes(r chi.Router) {
	r.Post("/{id}/calibrate", h.calibrate)
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
	// CleanPace marks a plottable pace sample (present, positive, and not
	// slower than the dropout threshold). The chart draws a gap where false;
	// the server owns the threshold so client and strip summary can't drift.
	CleanPace bool `json:"clean_pace"`
	// Latitude/Longitude are the WGS84 coordinates of this sample, or null
	// where the source trackpoint had no <Position>.
	//
	// These exist so the elevation profile and the route map can be ONE
	// instrument: the client's elevation strip is a straight map over this
	// array, so strip index i is trackpoint i, and scrubbing the profile to i
	// puts a marker at these coordinates. The simplified Route geometry cannot
	// serve that — it is RDP-reduced and deliberately not index-aligned with
	// anything. Answers sows/sow-trail-map.md Open Question 2.
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	// GradePercent is the signed slope at this sample, measured over a
	// smoothing window (see deriveGrades). Null where it cannot be measured
	// honestly. Derived on read, so it needs no migration and every historical
	// activity carries it immediately.
	GradePercent *float64 `json:"grade_percent"`
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
	Notes               *string         `json:"notes"`
	StartTime           time.Time       `json:"start_time"`
	DistanceMeters      float64         `json:"distance_meters"`
	RawDistanceMeters   float64         `json:"raw_distance_meters"`
	Environment         Environment     `json:"environment"`
	DurationSeconds     int             `json:"duration_seconds"`
	AvgPaceSecPerKm     *float64        `json:"avg_pace_sec_per_km"`
	BestPaceSecPerKm    *float64        `json:"best_pace_sec_per_km"`
	AvgHeartRateBpm     *int            `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm     *int            `json:"max_heart_rate_bpm"`
	TotalCalories       *int            `json:"total_calories"`
	ElevationGainMeters *float64        `json:"elevation_gain_meters"`
	ElevationLossMeters *float64        `json:"elevation_loss_meters"`
	ElevationHighMeters *float64        `json:"elevation_high_meters"`
	ElevationLowMeters  *float64        `json:"elevation_low_meters"`
	CreatedAt           time.Time       `json:"created_at"`
	Trackpoints         []trackpointDTO `json:"trackpoints,omitempty"`
	// HeartRateZones is the percent-of-max-HR time-in-zone breakdown. Populated
	// on the single-activity detail path for ANY activity type carrying usable
	// per-point HR — runs, hikes, and TCX-enriched lifts alike; omitempty drops
	// the key otherwise (no HR / engine unwired).
	HeartRateZones *heartRateZonesDTO `json:"heart_rate_zones,omitempty"`
	// Detail-only derived blocks (omitted on list responses and non-running
	// activities): the read-time derivation the detail page renders verbatim.
	// See sows/running-detail-metric-alignment.md.
	Unit               string               `json:"unit,omitempty"`
	Splits             []splitDTO           `json:"splits,omitempty"`
	StripSummary       *stripSummaryDTO     `json:"strip_summary,omitempty"`
	BestPaceSecPerUnit *float64             `json:"best_pace_sec_per_unit,omitempty"`
	Intervals          []intervalSegmentDTO `json:"intervals,omitempty"`
	// Route is the simplified GPS route as a GeoJSON Feature (MultiLineString
	// + bounds), passed through verbatim from storage. Detail-only; omitted
	// (key absent) when the activity has no route. The map consumes this; the
	// per-trackpoint DTO deliberately stays coordinate-free.
	Route json.RawMessage `json:"route,omitempty"`
	// Summary is the type's rendered card (registry Summarize): title,
	// subtitle, metric chips. Present on unified list items and detail
	// reads; omitted when no registry is wired or the row's type is
	// unregistered (degrade, don't fail).
	Summary *Summary `json:"summary,omitempty"`
	// Videos is the activity's attached videos, newest-ordered by position.
	// Detail-read only, and populated for EVERY activity type that has one —
	// gated on the data, never on activity_type. Nil (key absent) when video
	// storage is unwired, which is distinguishable from an empty array meaning
	// "storage is on, this activity has no videos".
	Videos *[]videoDTO `json:"videos,omitempty"`
	// Details is the type-keyed detail payload (registry DetailStore.Load):
	// exercises/sets + personal_records_set for strength, the detail-table
	// fields for endurance. Present on detail reads and, for types whose
	// store implements BulkDetailLoader (strength — /workouts list parity),
	// on list items too; omitted otherwise (endurance lists, base-only
	// shaped sessions, no registry wired).
	Details any `json:"details,omitempty"`
	// Photos is the activity's ordered (position, id) live photo set, each with
	// freshly presigned full + thumb URLs. Detail-only: attached solely on the
	// single-activity GET, never on list items or write read-backs. It is a
	// POINTER so the two "empty" cases stay distinguishable through omitempty:
	// when photo storage is configured the detail path assigns a non-nil
	// pointer (to a possibly-empty slice) so the key is ALWAYS present,
	// serializing as [] when the activity has no photos; when storage is
	// unconfigured it stays nil and omitempty drops the key entirely. A plain
	// slice cannot express this — omitempty drops an empty non-nil slice too.
	Photos *[]photoDTO `json:"photos,omitempty"`
}

// heartRateZoneDTO is one band of the five-zone model with its accumulated
// time. The wire keys match the SOW response shape exactly.
type heartRateZoneDTO struct {
	Zone        int     `json:"zone"`
	Name        string  `json:"name"`
	LowerPct    float64 `json:"lower_pct"`
	UpperPct    float64 `json:"upper_pct"`
	MinBpm      int     `json:"min_bpm"`
	MaxBpm      int     `json:"max_bpm"`
	TimeSeconds int     `json:"time_seconds"`
	TimePct     float64 `json:"time_pct"`
}

// heartRateZonesDTO is the heart_rate_zones block: the resolved reference, its
// provenance/confidence, and the per-zone breakdown.
type heartRateZonesDTO struct {
	Model               string             `json:"model"`
	MaxHRReferenceBpm   int                `json:"max_hr_reference_bpm"`
	ReferenceSource     string             `json:"reference_source"`
	ReferenceConfidence string             `json:"reference_confidence"`
	Calibrating         bool               `json:"calibrating"`
	TotalHRSeconds      int                `json:"total_hr_seconds"`
	Zones               []heartRateZoneDTO `json:"zones"`
}

// splitDTO is one distance bucket of the run's derived splits table. Pace is
// per DISPLAY UNIT (the response's `unit`), and by construction equals
// duration_seconds / distance_meters normalized to one unit — the invariant
// gate asserts it.
type splitDTO struct {
	Index                int      `json:"index"`
	Partial              bool     `json:"partial"`
	DistanceMeters       float64  `json:"distance_meters"`
	DurationSeconds      int      `json:"duration_seconds"`
	PaceSecPerUnit       *float64 `json:"pace_sec_per_unit"`
	AvgHRBpm             *float64 `json:"avg_hr_bpm"`
	ElevationDeltaMeters *float64 `json:"elevation_delta_meters"`
	Fastest              bool     `json:"fastest"`
	Slowest              bool     `json:"slowest"`
}

// stripSummaryDTO carries the pace-chart header numbers so the client renders
// text straight from the server (the chart line itself is a presentation
// mapping of the trackpoints, which are already in the payload — the strip is
// deliberately NOT duplicated as a parallel array; the detail response flows
// through MCP to the agent where doubling point data has real token cost).
type stripSummaryDTO struct {
	FastestSecPerUnit *float64 `json:"fastest_sec_per_unit"`
	SlowestSecPerUnit *float64 `json:"slowest_sec_per_unit"`
	DropoutCount      int      `json:"dropout_count"`
}

// intervalSegmentDTO is one labeled bout of a detected interval workout.
type intervalSegmentDTO struct {
	Kind            string   `json:"kind"`
	Rep             *int     `json:"rep"`
	Label           string   `json:"label"`
	DistanceMeters  float64  `json:"distance_meters"`
	DurationSeconds int      `json:"duration_seconds"`
	PaceSecPerUnit  *float64 `json:"pace_sec_per_unit"`
	AvgHRBpm        *float64 `json:"avg_hr_bpm"`
}

func toActivityDTO(a Activity, withTrackpoints bool) activityDTO {
	dto := activityDTO{
		ID:                  a.ID,
		ActivityType:        a.ActivityType,
		IngestSource:        a.IngestSource,
		SourceActivityID:    a.SourceActivityID,
		Name:                a.Name,
		Notes:               a.Notes,
		StartTime:           a.StartTime,
		DistanceMeters:      a.DistanceMeters,
		RawDistanceMeters:   a.RawDistanceMeters,
		Environment:         a.Environment,
		DurationSeconds:     a.DurationSeconds,
		AvgPaceSecPerKm:     a.AvgPaceSecPerKm,
		BestPaceSecPerKm:    a.BestPaceSecPerKm,
		AvgHeartRateBpm:     a.AvgHeartRateBpm,
		MaxHeartRateBpm:     a.MaxHeartRateBpm,
		TotalCalories:       a.TotalCalories,
		ElevationGainMeters: a.ElevationGainMeters,
		ElevationLossMeters: a.ElevationLossMeters,
		ElevationHighMeters: a.ElevationHighMeters,
		ElevationLowMeters:  a.ElevationLowMeters,
		CreatedAt:           a.CreatedAt,
	}
	if withTrackpoints {
		// One pass for the whole series, not one per point — grade at a sample
		// is a function of its neighbors, so it cannot be computed inside the
		// projection loop.
		grades := deriveGrades(a.Trackpoints)
		dto.Trackpoints = make([]trackpointDTO, 0, len(a.Trackpoints))
		for i, tp := range a.Trackpoints {
			dto.Trackpoints = append(dto.Trackpoints, trackpointDTO{
				Sequence:        tp.Sequence,
				ElapsedSeconds:  tp.ElapsedSeconds,
				DistanceMeters:  tp.DistanceMeters,
				HeartRateBpm:    tp.HeartRateBpm,
				PaceSecPerKm:    tp.PaceSecPerKm,
				ElevationMeters: tp.ElevationMeters,
				CleanPace:       isCleanTrackpointPace(tp.PaceSecPerKm),
				Latitude:        tp.Latitude,
				Longitude:       tp.Longitude,
				GradePercent:    grades[i],
			})
		}
	}
	if a.RouteGeoJSON != nil {
		dto.Route = json.RawMessage(*a.RouteGeoJSON)
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

	// Optional activity_type field pins the type instead of deriving it from
	// the TCX <Sport> tag (which can only say Running/Biking/Other). Validate
	// it up front so a bad value fails before we archive anything.
	var override ActivityType
	if raw := strings.TrimSpace(r.FormValue("activity_type")); raw != "" {
		t := ActivityType(raw)
		// A nil registry is a wiring bug, not a client error — matching the
		// other registry consumers in this package (unified_handler.go), fail
		// hard rather than silently misclassifying an unknown type as a 400.
		if h.registry == nil {
			httpresp.ServerError(w, r.Context(), "activity registry not wired", errors.New("SetRegistry not called"))
			return
		}
		if _, err := h.registry.Lookup(t); err != nil {
			// Unknown type: 422 listing the valid set (Lookup's message).
			httpresp.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Registered but non-endurance (e.g. strength_training): routing it
		// through the endurance summarizer would write a garbage detail row.
		// Strength ingest has its own path. detailTable(t) == "" marks base-only.
		if detailTable(t) == "" {
			httpresp.ErrorWithCode(w, http.StatusBadRequest,
				"activity_type "+raw+" cannot be ingested from a TCX file", "invalid_activity_type")
			return
		}
		override = t
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "missing file field in multipart upload", "unsupported_media_type")
		return
	}
	defer file.Close()

	rid := requestid.FromContext(r.Context())
	a, err := IngestTCX(r.Context(), h.repo, userID, IngestManualTCX, file, override)
	switch {
	case err == nil:
		log.Printf("activity import: request_id=%s user_id=%s source=%s source_activity_id=%s activity_type=%s outcome=imported",
			rid, userID, a.IngestSource, a.SourceActivityID, a.ActivityType)
		// Best-effort: publish the session into the timeline. Every ingested
		// type is a feed source — the source type is the coarse `activity`,
		// so an imported hike posts on the same path a run does. Never
		// affects this response. (The ErrDuplicate branch deliberately
		// doesn't publish — the post already exists from the original
		// ingest.)
		h.publish(r.Context(), timeline.PostRef{
			UserID:     a.UserID,
			SourceType: timeline.SourceActivity,
			SourceID:   a.ID,
			OccurredAt: a.StartTime,
		})
		// Best-effort milestone posts and planned-workout matching remain
		// running-only: best efforts are only computed for runs, and the
		// planner's endurance side models runs.
		if a.ActivityType == ActivityRunning {
			for _, be := range a.BestEfforts {
				h.publish(r.Context(), timeline.PostRef{
					UserID:     a.UserID,
					SourceType: timeline.SourceBestEffort,
					SourceID:   a.ID + ":" + be.DistanceKey,
					OccurredAt: a.StartTime,
				})
			}
			// Best-effort: link this run to the planned workout it completes.
			// Never affects this response.
			h.matchSession(r.Context(), a.UserID, SessionRef{SessionID: a.ID, StartUTC: a.StartTime})
		}
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

	// ?type= narrows the unified list to one registered type. Absent means
	// every type — strength_training included, per the unified-activity-model
	// SOW (the pre-042 exclusion is gone).
	var filter TypeFilter
	if raw := r.URL.Query().Get("type"); raw != "" {
		t := ActivityType(raw)
		if h.registry != nil {
			if _, err := h.registry.Lookup(t); err != nil {
				httpresp.Error(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		filter.Only = t
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
		activities, err := h.repo.ListInRange(r.Context(), userID, since, until, filter)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list activities in range", err)
			return
		}
		httpresp.OK(w, "listed activities", listResponse{Activities: h.toListDTOs(r.Context(), userID, activities), NextBefore: nil})
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

	activities, err := h.repo.List(r.Context(), userID, limit, before, filter)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list activities", err)
		return
	}

	var nextBefore *string
	if len(activities) == limit && len(activities) > 0 {
		cursor := activities[len(activities)-1].StartTime.Format(time.RFC3339)
		nextBefore = &cursor
	}

	httpresp.OK(w, "listed activities", listResponse{Activities: h.toListDTOs(r.Context(), userID, activities), NextBefore: nextBefore})
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

// parseUnitParam reads ?unit= for the detail derivation. Absent defaults to
// miles (the UI default); anything but mi/km is a client error.
func parseUnitParam(r *http.Request) (DistanceUnit, bool) {
	raw := r.URL.Query().Get("unit")
	if raw == "" {
		return UnitMiles, true
	}
	u := DistanceUnit(raw)
	return u, u.Valid()
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
	unit, ok := parseUnitParam(r)
	if !ok {
		httpresp.Error(w, http.StatusBadRequest, "unit must be 'mi' or 'km'")
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

	dto, err := h.buildDetailDTO(r.Context(), userID, *a, unit)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build activity detail", err)
		return
	}
	httpresp.OK(w, "fetched activity", dto)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
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
		Name        *string `json:"name"`
		Notes       *string `json:"notes"`
		Environment *string `json:"environment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == nil && req.Notes == nil && req.Environment == nil {
		httpresp.Error(w, http.StatusBadRequest, "name, notes, or environment is required")
		return
	}

	var updated *Activity
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
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
		updated = a
	}
	if req.Notes != nil {
		// Unlike name, a blank note is legitimate — it's how the client
		// clears one — so only the length is validated. The cap matches the
		// editor's own maxLength.
		notes := strings.TrimSpace(*req.Notes)
		if len(notes) > maxNotesLength {
			httpresp.Error(w, http.StatusBadRequest, "notes is too long")
			return
		}
		a, err := h.repo.UpdateNotes(r.Context(), userID, activityID, notes)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
				return
			}
			httpresp.ServerError(w, r.Context(), "update activity notes", err)
			return
		}
		updated = a
	}
	if req.Environment != nil {
		env := Environment(*req.Environment)
		if !env.Valid() {
			httpresp.ErrorWithCode(w, http.StatusBadRequest, "environment must be 'outdoor' or 'indoor'", "invalid_environment")
			return
		}
		a, err := h.repo.ChangeEnvironment(r.Context(), userID, activityID, env)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
				return
			}
			httpresp.ServerError(w, r.Context(), "change environment", err)
			return
		}
		updated = a
	}
	httpresp.OK(w, "updated activity", toActivityDTO(*updated, false))
}

func (h *Handler) calibrate(w http.ResponseWriter, r *http.Request) {
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
	unit, ok := parseUnitParam(r)
	if !ok {
		httpresp.Error(w, http.StatusBadRequest, "unit must be 'mi' or 'km'")
		return
	}
	var req struct {
		DistanceMeters float64 `json:"distance_meters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
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
	if a.ActivityType != ActivityRunning {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "only running activities can be calibrated", "not_a_running_activity")
		return
	}
	if a.Environment != EnvironmentIndoor {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "tag the run as indoor before calibrating its distance", "outdoor_run_not_calibratable")
		return
	}
	if req.DistanceMeters <= 0 {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "distance_meters must be greater than zero", "invalid_calibration_distance")
		return
	}
	if a.DistanceMeters <= 0 {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "activity has no distance to calibrate from", "invalid_calibration_distance")
		return
	}
	f := req.DistanceMeters / a.DistanceMeters
	if f < calibrationMinFactor || f > calibrationMaxFactor {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "calibrated distance implies an implausible correction (must be between 0.5x and 2x the current distance)", "calibration_out_of_range")
		return
	}

	updated, err := h.repo.Calibrate(r.Context(), userID, activityID, req.DistanceMeters)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "calibrate activity", err)
		return
	}
	dto, err := h.buildDetailDTO(r.Context(), userID, *updated, unit)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build activity detail", err)
		return
	}
	httpresp.OK(w, "calibrated activity", dto)
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
	// This is a SOFT delete: the row (and its photo rows, whose FK cascade
	// only fires on a HARD delete) survives untouched, deleted_at just hides
	// it. Deliberately NO photo-object orphan-tagging here — a soft delete is
	// reversible, and restoring the activity must bring its photos back with
	// their objects intact. The soft-deleted parent already hides the photos
	// from every read (ListByActivity / CoverPhotosByActivityIDs JOIN
	// activities and require a.deleted_at IS NULL), so nothing leaks; tagging
	// would let the lifecycle rule reap still-referenced objects.
	if err := h.repo.SoftDelete(r.Context(), userID, activityID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete activity", err)
		return
	}
	// Best-effort: revert any plan link for the deleted activity. Never
	// affects this response.
	h.unmatchSession(r.Context(), userID, activityID)
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
	demographics := h.loadDemographics(r.Context(), userID, now)

	distances := make([]maxEffortSummaryEntryDTO, 0, len(StandardDistances))
	for _, d := range StandardDistances {
		var loggedBest *float64
		if b, ok := bestByKey[d.Key]; ok {
			s := b.DurationSeconds
			loggedBest = &s
		}
		res := est.Estimate(estimate.EstimateInput{
			TargetDistanceKey:    d.Key,
			TargetDistanceMeters: d.Meters,
			Attempts:             attempts,
			Demographics:         demographics,
			Now:                  now,
			LoggedBestSeconds:    loggedBest,
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
	ActivityID      string   `json:"activity_id"`
	AchievedAt      string   `json:"achieved_at"`
	DurationSeconds float64  `json:"duration_seconds"`
	PaceSecPerKm    float64  `json:"pace_sec_per_km"`
	Source          string   `json:"source"`
	PaceRatio       *float64 `json:"pace_ratio,omitempty"`
	HRZ4Z5Pct       *float64 `json:"hr_z4_z5_pct,omitempty"`
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
	Seconds             float64 `json:"seconds"`
	LowerSeconds        float64 `json:"lower_seconds"`
	UpperSeconds        float64 `json:"upper_seconds"`
	RawSeconds          float64 `json:"raw_seconds,omitempty"`
	FlooredAtLoggedBest bool    `json:"floored_at_logged_best,omitempty"`
	Basis               string  `json:"basis"`
	Confidence          string  `json:"confidence"`
	NPoints             int     `json:"n_points"`
	NDistances          int     `json:"n_distances"`
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
	demographics := h.loadDemographics(r.Context(), userID, now)

	bests, err := h.repo.GetUserRunningBestEfforts(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get running best efforts", err)
		return
	}
	var loggedBest *float64
	for _, b := range bests {
		if b.DistanceKey == d.Key {
			s := b.DurationSeconds
			loggedBest = &s
			break
		}
	}

	res := est.Estimate(estimate.EstimateInput{
		TargetDistanceKey:    d.Key,
		TargetDistanceMeters: d.Meters,
		Attempts:             attempts,
		Demographics:         demographics,
		Now:                  now,
		LoggedBestSeconds:    loggedBest,
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
			Demographics:         demographics,
			Now:                  t,
			LoggedBestSeconds:    loggedBestAtDistance(filtered, d.Key),
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
		dto := maxEffortAttemptDTO{
			ActivityID:      "",
			AchievedAt:      a.AchievedAt.Format(time.RFC3339),
			DurationSeconds: a.DurationSeconds,
			PaceSecPerKm:    a.DurationSeconds / (d.Meters / 1000),
			Source:          estimate.ClassifySource(d.Meters, a.ActivityDistanceMeters),
		}
		if a.ActivityAvgPaceSecPerKm != nil && *a.ActivityAvgPaceSecPerKm > 0 {
			ratio := (a.DurationSeconds / (d.Meters / 1000)) / *a.ActivityAvgPaceSecPerKm
			dto.PaceRatio = &ratio
		}
		if a.HRZoneHighIntensityPct != nil {
			dto.HRZ4Z5Pct = a.HRZoneHighIntensityPct
		}
		attemptDTOs = append(attemptDTOs, dto)
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

	// actual_best at this distance (reuse bests loaded above).
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
			Seconds:             res.Seconds,
			LowerSeconds:        res.LowerSeconds,
			UpperSeconds:        res.UpperSeconds,
			RawSeconds:          res.RawSeconds,
			FlooredAtLoggedBest: res.FlooredAtLoggedBest,
			Basis:               res.Basis,
			Confidence:          res.Confidence,
			NPoints:             res.NPoints,
			NDistances:          res.NDistances,
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
