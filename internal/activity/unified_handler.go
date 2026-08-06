package activity

// The unified /activities surface: the type-agnostic create/update/read
// paths that dispatch per-type behavior through the registry (see
// registry.go and the unified-activity-model SOW). Split from handler.go,
// which keeps the TCX ingest, list/patch/delete plumbing, and the running
// analytics endpoints.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/hrzones"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/timeline"
)

// toListDTOs renders one page of unified list items: the base DTO (no
// trackpoints) plus each type's Summary, and — for types whose store bulk-
// loads details (strength) — the typed details payload itself. Attaching
// the already-loaded details gives the list /workouts parity (exercises +
// personal_records_set per item) for free: the same single bulk read powers
// both the summary render and the embed (see summary.go).
func (h *Handler) toListDTOs(ctx context.Context, userID string, activities []Activity) []activityDTO {
	out := make([]activityDTO, 0, len(activities))
	for _, a := range activities {
		out = append(out, toActivityDTO(a, false))
	}
	details := LoadDetailsBulk(ctx, h.registry, userID, activities)
	for i, a := range activities {
		if d, ok := details[a.ID]; ok {
			out[i].Details = d
		}
		if s, ok := RenderSummary(h.registry, a, details[a.ID]); ok {
			out[i].Summary = &s
		}
	}
	return out
}

// buildDetailDTO renders the full single-activity detail shape: the base DTO
// with trackpoints plus — when the engine is wired and HR is usable — the
// heart_rate_zones block, and, for running activities, the read-time derived
// blocks (splits, strip summary, best pace, intervals). Shared by the detail
// GET and the calibrate response so both return an identical shape.
func (h *Handler) buildDetailDTO(ctx context.Context, userID string, a Activity, unit DistanceUnit) (activityDTO, error) {
	dto := toActivityDTO(a, true)
	if err := h.attachTypedPayload(ctx, userID, a, &dto); err != nil {
		return activityDTO{}, err
	}
	if err := h.attachPhotos(ctx, userID, a.ID, &dto); err != nil {
		return activityDTO{}, err
	}
	if err := h.attachHeartRateZones(ctx, userID, a, &dto); err != nil {
		return activityDTO{}, err
	}
	if err := h.attachVideos(ctx, userID, a.ID, &dto); err != nil {
		return activityDTO{}, err
	}
	// Read-time derivation + invariant gate (running only). Violations are
	// ERROR-logged but the response is still served: a read never 500s over
	// an accounting mismatch — CI fixtures assert the gate stays quiet.
	if a.ActivityType == ActivityRunning && len(a.Trackpoints) >= 2 {
		der := deriveRunning(a.Trackpoints, unit)
		dto.Unit = string(unit)
		dto.Splits = make([]splitDTO, 0, len(der.Splits))
		for _, s := range der.Splits {
			dto.Splits = append(dto.Splits, splitDTO{
				Index: s.Index, Partial: s.Partial,
				DistanceMeters: s.DistanceMeters, DurationSeconds: s.DurationSeconds,
				PaceSecPerUnit: s.PaceSecPerUnit, AvgHRBpm: s.AvgHRBpm,
				ElevationDeltaMeters: s.ElevDeltaMeters,
				Fastest:              s.Fastest, Slowest: s.Slowest,
			})
		}
		dto.StripSummary = &stripSummaryDTO{
			FastestSecPerUnit: der.StripSummary.FastestSecPerUnit,
			SlowestSecPerUnit: der.StripSummary.SlowestSecPerUnit,
			DropoutCount:      der.StripSummary.DropoutCount,
		}
		dto.BestPaceSecPerUnit = der.BestPaceSecPerUnit
		if len(der.Intervals) > 0 {
			dto.Intervals = make([]intervalSegmentDTO, 0, len(der.Intervals))
			for _, seg := range der.Intervals {
				dto.Intervals = append(dto.Intervals, intervalSegmentDTO(seg))
			}
		}
		for _, violation := range checkDetailInvariants(a, der, unit, dto.HeartRateZones) {
			log.Printf("ERROR activity detail invariant violation: activity_id=%s %s", a.ID, violation)
		}
	}
	return dto, nil
}

// attachHeartRateZones fills the detail DTO's heart_rate_zones block for ANY
// activity type that carries per-point heart rate — a run, a hike, a lift with
// a Garmin TCX attached. Time in zone is a property of the heart-rate stream,
// not of the sport, so the block is gated on the data (the engine being wired
// and Compute finding at least one HR-bearing interval) rather than on
// a.ActivityType. Activities with no HR simply leave the key absent, exactly as
// a no-HR run always has.
//
// The reference max HR stays RUNNING-derived (RecentHRStats scans running rows
// only): max HR is a property of the person, not the session, and runs are the
// highest-intensity signal available. Pooling a lift's thousands of
// low-intensity samples into the p99 would drag the reference down and inflate
// every activity's zone times. A hike or lift still contributes its OWN p99 as
// CurrentRunP99, so a maximal effort outside running can still raise a
// cold-start reference — the same rung of the ladder a first run climbs.
func (h *Handler) attachHeartRateZones(ctx context.Context, userID string, a Activity, dto *activityDTO) error {
	if h.hrEngine == nil {
		return nil
	}
	tps := make([]hrzones.Trackpoint, 0, len(a.Trackpoints))
	currentHRSamples := make([]int, 0, len(a.Trackpoints))
	for _, tp := range a.Trackpoints {
		tps = append(tps, hrzones.Trackpoint{ElapsedSeconds: tp.ElapsedSeconds, HeartRateBpm: tp.HeartRateBpm})
		if tp.HeartRateBpm != nil {
			currentHRSamples = append(currentHRSamples, *tp.HeartRateBpm)
		}
	}
	// No HR anywhere in the stream: skip the reference read entirely rather
	// than paying for a trackpoint scan Compute would only reject.
	if len(currentHRSamples) == 0 {
		return nil
	}
	stats, err := h.repo.RecentHRStats(ctx, userID, h.hrWindow, a.ID)
	if err != nil {
		return err
	}
	stats.CurrentRunP99 = hrzones.P99(currentHRSamples)
	ref := h.hrEngine.EstimateReference(stats)
	res, ok := h.hrEngine.Compute(ref, tps)
	if !ok {
		return nil
	}
	zones := make([]heartRateZoneDTO, 0, len(res.Zones))
	for _, z := range res.Zones {
		zones = append(zones, heartRateZoneDTO{
			Zone: z.Number, Name: z.Name, LowerPct: z.LowerPct, UpperPct: z.UpperPct,
			MinBpm: z.MinBpm, MaxBpm: z.MaxBpm, TimeSeconds: z.TimeSeconds, TimePct: z.TimePct,
		})
	}
	dto.HeartRateZones = &heartRateZonesDTO{
		Model: res.Model, MaxHRReferenceBpm: res.Reference.MaxHRBpm,
		ReferenceSource: res.Reference.Source, ReferenceConfidence: string(res.Reference.Confidence),
		Calibrating: res.Calibrating, TotalHRSeconds: res.TotalHRSeconds, Zones: zones,
	}
	return nil
}

// attachTypedPayload adds the registry-driven fields to a detail-read DTO:
// the typed details payload from the type's DetailStore and the rendered
// Summary. A base-only shaped session (no detail row) omits details —
// ErrNotFound from Load after the base read already succeeded can only mean
// "no detail row here". No registry (legacy construction) attaches nothing.
func (h *Handler) attachTypedPayload(ctx context.Context, userID string, a Activity, dto *activityDTO) error {
	if h.registry == nil {
		return nil
	}
	d, err := h.registry.Lookup(a.ActivityType)
	if err != nil {
		return nil
	}
	var details any
	if d.Details != nil {
		details, err = d.Details.Load(ctx, userID, a.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	dto.Details = details
	if s, ok := RenderSummary(h.registry, a, details); ok {
		dto.Summary = &s
	}
	return nil
}

// attachPhotos populates the detail DTO's Photos array with the activity's live
// photos, ordered (position, id) and filtered to live + parent-live by the repo,
// each carrying freshly presigned full + thumb URLs. Detail-only: called from
// buildDetailDTO so only GET /activities/{id} carries photos — list items and
// write read-backs never do. When photo storage is unconfigured (either the
// store or the repo is nil) it leaves dto.Photos nil so omitempty drops the key
// entirely; when configured it always assigns a non-nil slice, so the key is
// present and serializes as [] for an activity with no photos.
func (h *Handler) attachPhotos(ctx context.Context, userID, activityID string, dto *activityDTO) error {
	if h.photoRepo == nil || h.photoStore == nil {
		return nil
	}
	photos, err := h.photoRepo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		return err
	}
	dtos := make([]photoDTO, 0, len(photos))
	for _, p := range photos {
		pd, err := h.toPhotoDTO(ctx, p)
		if err != nil {
			return err
		}
		dtos = append(dtos, pd)
	}
	dto.Photos = &dtos
	return nil
}

// --- unified create / update ---------------------------------------------

// createActivityRequest is the POST/PUT /activities body: the activities
// base columns plus the type-keyed details blob each descriptor validates.
//
// Vitals (avg/max HR, calories) are optional and, on PUT, absent means
// "preserve what's stored" rather than clear: a session's vitals usually
// come from its TCX enrichment, not the client's edit form, so a rename-
// style PUT must not strip them (there is deliberately no way to null
// vitals through this surface — re-attach or detach the TCX instead).
// Present values replace.
type createActivityRequest struct {
	ActivityType    string          `json:"activity_type"`
	StartTime       string          `json:"start_time"` // RFC3339
	DurationSeconds *int            `json:"duration_seconds"`
	Name            *string         `json:"name"`
	Notes           *string         `json:"notes"`
	AvgHeartRateBpm *int            `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm *int            `json:"max_heart_rate_bpm"`
	TotalCalories   *int            `json:"total_calories"`
	Details         json.RawMessage `json:"details"`
}

// decodeCreateRequest decodes + base-validates the unified write body and
// resolves its descriptor. Base-field validation (type known, start_time
// present, duration sane) is the handler's job; everything inside details
// is the descriptor's. Writes the error response itself and returns ok=false
// on failure.
func (h *Handler) decodeCreateRequest(w http.ResponseWriter, r *http.Request) (CreateRequest, *Descriptor, bool) {
	var body createActivityRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return CreateRequest{}, nil, false
	}
	if body.ActivityType == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity_type is required")
		return CreateRequest{}, nil, false
	}
	desc, err := h.registry.Lookup(ActivityType(body.ActivityType))
	if err != nil {
		// Unknown type is a 422 (well-formed request, unregistered type)
		// listing the valid set — the registry's error text carries it.
		httpresp.Error(w, http.StatusUnprocessableEntity, err.Error())
		return CreateRequest{}, nil, false
	}
	if body.StartTime == "" {
		httpresp.Error(w, http.StatusBadRequest, "start_time is required")
		return CreateRequest{}, nil, false
	}
	start, err := time.Parse(time.RFC3339, body.StartTime)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid start_time: must be RFC3339 format")
		return CreateRequest{}, nil, false
	}
	if body.DurationSeconds != nil && *body.DurationSeconds < 0 {
		httpresp.Error(w, http.StatusBadRequest, "duration_seconds must not be negative")
		return CreateRequest{}, nil, false
	}
	req := CreateRequest{
		Type:            desc.Type,
		StartTime:       start,
		DurationSeconds: body.DurationSeconds,
		Name:            body.Name,
		Notes:           body.Notes,
		AvgHeartRateBpm: body.AvgHeartRateBpm,
		MaxHeartRateBpm: body.MaxHeartRateBpm,
		TotalCalories:   body.TotalCalories,
		Details:         body.Details,
	}
	if desc.ValidateCreate != nil {
		if err := desc.ValidateCreate(req); err != nil {
			// Every ValidateCreate failure — ErrInvalidDetails,
			// ErrDistanceRequired, a type's own validation sentinel — is a
			// 400 with the first error's message, matching the existing
			// handlers' first-error-wins boundary validation.
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return CreateRequest{}, nil, false
		}
	}
	return req, desc, true
}

// respondWriteError maps a create/update persistence error onto the
// existing handler conventions: 404 not found, 409 duplicate, 400 for
// descriptor-translated validation/detail errors, 500 otherwise.
func respondWriteError(w http.ResponseWriter, r *http.Request, action string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
	case errors.Is(err, ErrDuplicate):
		httpresp.ErrorWithCode(w, http.StatusConflict, "an activity for this source already exists", "duplicate_activity")
	case errors.Is(err, ErrInvalidDetails):
		httpresp.ErrorWithCode(w, http.StatusBadRequest, err.Error(), "invalid_details")
	case errors.Is(err, ErrDistanceRequired):
		httpresp.ErrorWithCode(w, http.StatusBadRequest, err.Error(), "distance_required")
	default:
		httpresp.ServerError(w, r.Context(), action, err)
	}
}

// createUnified handles POST /activities: the one create endpoint for every
// registered type. The descriptor decides the write path — strength routes
// through its own Create (workout repo: PR detection, 1RM history, timeline
// posts); everything else is a base-row insert plus a DetailStore save.
func (h *Handler) createUnified(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	if h.registry == nil {
		httpresp.ServerError(w, r.Context(), "activity registry not wired", errors.New("SetRegistry not called"))
		return
	}
	req, desc, ok := h.decodeCreateRequest(w, r)
	if !ok {
		return
	}

	var activityID string
	var err error
	if desc.Create != nil {
		activityID, err = desc.Create(r.Context(), userID, req)
		if err != nil {
			respondWriteError(w, r, "create activity", err)
			return
		}
	} else {
		activityID, err = h.repo.CreateManual(r.Context(), userID, req)
		if err != nil {
			respondWriteError(w, r, "create activity", err)
			return
		}
		if err := h.saveDecodedDetails(r.Context(), userID, activityID, desc, req); err != nil {
			// The base row is in but its details didn't land: roll the
			// manual create back (best-effort) so a failed write never
			// leaves a half-logged session behind.
			_ = h.repo.SoftDelete(r.Context(), userID, activityID)
			respondWriteError(w, r, "save activity details", err)
			return
		}
		// Best-effort side effects, mirroring the TCX ingest path. EVERY
		// session type posts to the feed — the source type is the coarse
		// `activity`, so there is no per-sport gate to keep in sync as
		// types are registered (a hike used to fall through this branch and
		// silently never reach the feed).
		h.publish(r.Context(), timeline.PostRef{
			UserID:     userID,
			SourceType: timeline.SourceActivity,
			SourceID:   activityID,
			OccurredAt: req.StartTime,
		})
		// Planned-workout matching stays running-only: the planner's
		// endurance side models runs, and widening it is a product decision
		// for the reconciliation SOW, not a side effect of feed coverage.
		if desc.Type == ActivityRunning {
			h.matchSession(r.Context(), userID, SessionRef{SessionID: activityID, StartUTC: req.StartTime})
		}
	}

	dto, ok := h.unifiedReadDTO(w, r, userID, activityID)
	if !ok {
		return
	}
	httpresp.Created(w, "created activity", dto)
}

// updateUnified handles PUT /activities/{id}: full-replacement typed update
// of the user-editable fields. The row's stored type picks the descriptor —
// a PUT can't change a session's type. Vitals are the one exception to full
// replacement: absent means preserve (see createActivityRequest) so an
// edit never strips TCX-derived HR/calories while the HR trackpoints
// remain. The strength path gets the same guarantee from the workout
// repository, whose update never touches enrichment columns.
func (h *Handler) updateUnified(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	if h.registry == nil {
		httpresp.ServerError(w, r.Context(), "activity registry not wired", errors.New("SetRegistry not called"))
		return
	}
	activityID := chi.URLParam(r, "id")
	if activityID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id is required")
		return
	}
	existing, err := h.repo.Get(r.Context(), userID, activityID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get activity", err)
		return
	}
	req, desc, ok := h.decodeCreateRequest(w, r)
	if !ok {
		return
	}
	if desc.Type != existing.ActivityType {
		httpresp.Error(w, http.StatusBadRequest, "activity_type cannot be changed on update")
		return
	}

	if desc.Update != nil {
		if err := desc.Update(r.Context(), userID, activityID, req); err != nil {
			respondWriteError(w, r, "update activity", err)
			return
		}
	} else {
		// Absent vitals preserve the stored values (typically TCX-derived);
		// explicit values replace them.
		if req.AvgHeartRateBpm == nil {
			req.AvgHeartRateBpm = existing.AvgHeartRateBpm
		}
		if req.MaxHeartRateBpm == nil {
			req.MaxHeartRateBpm = existing.MaxHeartRateBpm
		}
		if req.TotalCalories == nil {
			req.TotalCalories = existing.TotalCalories
		}
		if err := h.repo.UpdateBase(r.Context(), userID, activityID, req); err != nil {
			respondWriteError(w, r, "update activity", err)
			return
		}
		if err := h.saveDecodedDetails(r.Context(), userID, activityID, desc, req); err != nil {
			respondWriteError(w, r, "save activity details", err)
			return
		}
	}

	dto, ok := h.unifiedReadDTO(w, r, userID, activityID)
	if !ok {
		return
	}
	httpresp.OK(w, "updated activity", dto)
}

// saveDecodedDetails runs the generic details write: decode the blob via the
// descriptor and full-replace through its DetailStore. An absent blob means
// "no details" — on update the existing detail row is removed to honor
// full-replacement semantics (idempotent for types that never had one).
func (h *Handler) saveDecodedDetails(ctx context.Context, userID, activityID string, desc *Descriptor, req CreateRequest) error {
	if desc.Details == nil || desc.DecodeDetails == nil {
		return nil
	}
	decoded, err := desc.DecodeDetails(req.Details)
	if err != nil {
		return err
	}
	if decoded == nil {
		return desc.Details.Delete(ctx, userID, activityID)
	}
	return desc.Details.Save(ctx, userID, activityID, decoded)
}

// unifiedReadDTO re-reads a just-written activity and renders the unified
// detail shape (base + summary + details, no trackpoint derivations — the
// write paths never carry a track). Writes the error response itself and
// returns ok=false on failure.
func (h *Handler) unifiedReadDTO(w http.ResponseWriter, r *http.Request, userID, activityID string) (activityDTO, bool) {
	a, err := h.repo.Get(r.Context(), userID, activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "read back activity", err)
		return activityDTO{}, false
	}
	dto := toActivityDTO(*a, false)
	if err := h.attachTypedPayload(r.Context(), userID, *a, &dto); err != nil {
		httpresp.ServerError(w, r.Context(), "load activity details", err)
		return activityDTO{}, false
	}
	return dto, true
}
