package workout

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// maxTCXUploadBytes caps the multipart upload size, matching the run
// importer's 10 MB ceiling. The whole file is read into memory to parse and
// archive, so the cap bounds per-request memory.
const maxTCXUploadBytes = 10 << 20

// --- enrichment DTO ----------------------------------------------------

// workoutEnrichmentDTO is the heart-rate / effort layer embedded in a
// workout DTO when a Garmin TCX is attached. It is a projection of the
// linked activity: HR/calorie summary always, plus the downsampled HR
// trackpoints on the single-workout detail load only (omitted on list).
type workoutEnrichmentDTO struct {
	SourceActivityID string                   `json:"source_activity_id"`
	StartTime        time.Time                `json:"start_time"`
	DurationSeconds  int                      `json:"duration_seconds"`
	AvgHeartRateBpm  *int                     `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm  *int                     `json:"max_heart_rate_bpm"`
	TotalCalories    *int                     `json:"total_calories"`
	Trackpoints      []workoutHRTrackpointDTO `json:"trackpoints,omitempty"`
}

// workoutHRTrackpointDTO is one downsampled chart point on the elapsed-time
// HR axis. Distance/pace/elevation are meaningless for a strength session and
// deliberately absent from the wire shape.
type workoutHRTrackpointDTO struct {
	Sequence       int  `json:"sequence"`
	ElapsedSeconds int  `json:"elapsed_seconds"`
	HeartRateBpm   *int `json:"heart_rate_bpm"`
}

// toWorkoutEnrichment projects an activity into the workout enrichment DTO.
// withTrackpoints is true only on the detail path; the list path passes false
// so the per-second stream never ships in a page of workouts.
func toWorkoutEnrichment(a activity.Activity, withTrackpoints bool) *workoutEnrichmentDTO {
	e := &workoutEnrichmentDTO{
		SourceActivityID: a.SourceActivityID,
		StartTime:        a.StartTime,
		DurationSeconds:  a.DurationSeconds,
		AvgHeartRateBpm:  a.AvgHeartRateBpm,
		MaxHeartRateBpm:  a.MaxHeartRateBpm,
		TotalCalories:    a.TotalCalories,
	}
	if withTrackpoints {
		e.Trackpoints = make([]workoutHRTrackpointDTO, 0, len(a.Trackpoints))
		for _, tp := range a.Trackpoints {
			e.Trackpoints = append(e.Trackpoints, workoutHRTrackpointDTO{
				Sequence:       tp.Sequence,
				ElapsedSeconds: tp.ElapsedSeconds,
				HeartRateBpm:   tp.HeartRateBpm,
			})
		}
	}
	return e
}

// attachEnrichment fills each workout's Enrichment from its linked activity.
// withTrackpoints picks the load strategy: the detail path loads each
// activity individually (with its trackpoint stream); the list path batches
// the summaries in a single read and omits trackpoints. A dangling link (the
// activity is missing/soft-deleted) leaves Enrichment nil so the workout
// still renders without a card rather than failing the request.
func (h *Handler) attachEnrichment(ctx context.Context, workouts []workoutWithEvents, withTrackpoints bool) error {
	if withTrackpoints {
		for i := range workouts {
			aid := workouts[i].ActivityID
			if aid == nil {
				continue
			}
			a, err := h.activityRepo.Get(ctx, workouts[i].UserID, *aid)
			if err != nil {
				if errors.Is(err, activity.ErrNotFound) {
					continue
				}
				return err
			}
			workouts[i].Enrichment = toWorkoutEnrichment(*a, true)
		}
		return nil
	}

	var ids []string
	for i := range workouts {
		if workouts[i].ActivityID != nil {
			ids = append(ids, *workouts[i].ActivityID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// The list endpoint scopes to one user (ListByUser), so every workout here
	// shares an owner; take it once rather than from the last linked workout.
	summaries, err := h.activityRepo.SummariesByIDs(ctx, workouts[0].UserID, ids)
	if err != nil {
		return err
	}
	for i := range workouts {
		aid := workouts[i].ActivityID
		if aid == nil {
			continue
		}
		if a, ok := summaries[*aid]; ok {
			workouts[i].Enrichment = toWorkoutEnrichment(a, false)
		}
	}
	return nil
}

// detailDTO builds the single-workout response: PR events plus the
// enrichment with trackpoints. Used by the import/attach responses and the
// detail GET.
func (h *Handler) detailDTO(ctx context.Context, w *Workout) (workoutWithEvents, error) {
	wrapped, err := h.attachPersonalRecordEvents(ctx, []Workout{*w})
	if err != nil {
		return workoutWithEvents{}, err
	}
	if err := h.attachEnrichment(ctx, wrapped, true); err != nil {
		return workoutWithEvents{}, err
	}
	return wrapped[0], nil
}

// --- handlers ----------------------------------------------------------

// importFromTCX handles POST /workouts/imports — the "Log from TCX" path. It
// mints an EMPTY workout from a Garmin strength TCX: the file becomes a
// strength_training activity, and a zero-exercise workout is created pointing
// at it with performed_at = the TCX start and ended_at = start + duration.
// The user fills in exercises afterward on the detail page.
func (h *Handler) importFromTCX(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	file, ok := readTCXUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()

	a, ok := h.ingestTCXActivity(w, r, userID, "", file)
	if !ok {
		return
	}

	activityID := a.ID
	endedAt := a.StartTime.Add(time.Duration(a.DurationSeconds) * time.Second)
	workout := &Workout{
		UserID:      userID,
		Name:        fmt.Sprintf("Workout - %s", a.StartTime.Format("Jan 02, 2006")),
		PerformedAt: a.StartTime,
		EndedAt:     &endedAt,
		ActivityID:  &activityID,
		Exercises:   []WorkoutExercise{},
	}
	if err := h.repo.Create(r.Context(), workout); err != nil {
		// The activity already committed in its own transaction; clean it up
		// best-effort so a failed import doesn't leave an orphan that blocks a
		// re-upload via the dedup index. Ignore the cleanup error — the 500 is
		// the load-bearing signal.
		_ = h.activityRepo.SoftDelete(r.Context(), userID, a.ID)
		httpresp.ServerError(w, r.Context(), "create workout from tcx", err)
		return
	}

	// Best-effort: a new workout publishes its timeline post and fires the
	// workout plan matcher, exactly like any other create. The activity plan
	// matcher is deliberately NOT fired — the workout is the reconciliation
	// signal, and double-firing would double-count the planned session.
	h.publishWorkoutPosts(r.Context(), workout)
	h.matchSession(r.Context(), workout.UserID, SessionRef{SessionID: workout.ID, StartUTC: workout.PerformedAt})

	log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s source_activity_id=%s outcome=imported",
		requestid.FromContext(r.Context()), userID, workout.ID, a.SourceActivityID)

	dto, err := h.detailDTO(r.Context(), workout)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build workout dto", err)
		return
	}
	httpresp.Created(w, "imported workout from tcx", dto)
}

// attachTCX handles POST /workouts/{id}/tcx — the retroactive attach. It
// loads the workout, rejects a second file (409 workout_tcx_exists), then
// ingests the TCX as a strength_training activity and points the workout at
// it. performed_at/ended_at are left untouched (the user logged those).
func (h *Handler) attachTCX(w http.ResponseWriter, r *http.Request) {
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

	existing, ok := h.loadOwnedWorkout(w, r, userID, workoutID)
	if !ok {
		return
	}
	if existing.ActivityID != nil {
		httpresp.ErrorWithCode(w, http.StatusConflict,
			"this workout already has a file attached — detach it first", "workout_tcx_exists")
		return
	}

	file, ok := readTCXUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()

	a, ok := h.ingestTCXActivity(w, r, userID, workoutID, file)
	if !ok {
		return
	}

	if err := h.repo.AttachActivity(r.Context(), userID, workoutID, a.ID, time.Now().UTC()); err != nil {
		// The activity committed in its own transaction; if linking it to the
		// workout fails (e.g. the workout was deleted in a race), soft-delete
		// it best-effort so it doesn't orphan and block a re-attach.
		_ = h.activityRepo.SoftDelete(r.Context(), userID, a.ID)
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "attach activity", err)
		return
	}

	log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s source_activity_id=%s outcome=attached",
		requestid.FromContext(r.Context()), userID, workoutID, a.SourceActivityID)

	updated, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reload workout", err)
		return
	}
	dto, err := h.detailDTO(r.Context(), updated)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build workout dto", err)
		return
	}
	httpresp.OK(w, "attached tcx", dto)
}

// detachTCX handles DELETE /workouts/{id}/tcx. It clears the workout's
// activity_id and soft-deletes the linked activity (retaining its trackpoints
// and archived file). Idempotent: a workout with no TCX attached returns 204
// without touching anything. It never fires either plan matcher — the workout
// itself is unchanged.
func (h *Handler) detachTCX(w http.ResponseWriter, r *http.Request) {
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

	existing, ok := h.loadOwnedWorkout(w, r, userID, workoutID)
	if !ok {
		return
	}
	if existing.ActivityID == nil {
		// Nothing attached — idempotent no-op.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.repo.DetachActivity(r.Context(), userID, workoutID, *existing.ActivityID, time.Now().UTC()); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "detach activity", err)
		return
	}

	log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s source_activity_id=%s outcome=detached",
		requestid.FromContext(r.Context()), userID, workoutID, *existing.ActivityID)

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers -----------------------------------------------------------

// loadOwnedWorkout fetches a workout and enforces ownership, writing the 404
// (missing or not-owned, indistinguishable to prevent id enumeration) or 500
// itself and returning ok=false when it does.
func (h *Handler) loadOwnedWorkout(w http.ResponseWriter, r *http.Request, userID, workoutID string) (*Workout, bool) {
	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return nil, false
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return nil, false
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return nil, false
	}
	return existing, true
}

// readTCXUpload reads the "file" field from a multipart request, capping the
// body at 10 MB. It writes the 413 file_too_large / 415 unsupported_media_type
// response itself and returns ok=false on failure. The caller must Close the
// returned file.
func readTCXUpload(w http.ResponseWriter, r *http.Request) (multipart.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTCXUploadBytes)
	if err := r.ParseMultipartForm(maxTCXUploadBytes); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return nil, false
		}
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "expected a multipart upload with a file field", "unsupported_media_type")
		return nil, false
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "missing file field in multipart upload", "unsupported_media_type")
		return nil, false
	}
	return file, true
}

// ingestTCXActivity runs the strength-TCX ingest and maps its error modes to
// HTTP responses, writing the response itself and returning ok=false when the
// ingest fails. On success it returns the persisted activity. workoutID is
// "" for the create-from-TCX path (no workout exists yet) and used only for
// the structured log line.
func (h *Handler) ingestTCXActivity(w http.ResponseWriter, r *http.Request, userID, workoutID string, file multipart.File) (activity.Activity, bool) {
	rid := requestid.FromContext(r.Context())
	a, err := activity.IngestStrengthTCX(r.Context(), h.activityRepo, userID, file)
	switch {
	case err == nil:
		return a, true
	case errors.Is(err, activity.ErrDuplicate):
		existing := h.resolveDuplicate(r.Context(), userID, a)
		log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s source_activity_id=%s outcome=duplicate",
			rid, userID, workoutID, a.SourceActivityID)
		httpresp.ErrorWithCodeData(w, http.StatusConflict, "this file is already in your log", "duplicate_activity",
			map[string]any{"existing": existing})
		return activity.Activity{}, false
	case errors.Is(err, activity.ErrStorage):
		log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s outcome=storage_failed err=%v",
			rid, userID, workoutID, err)
		httpresp.ErrorWithCode(w, http.StatusInternalServerError, "failed to archive tcx file", "storage_failed")
		return activity.Activity{}, false
	default:
		var verr *activity.ValidationError
		if errors.As(err, &verr) {
			log.Printf("workout tcx: request_id=%s user_id=%s workout_id=%s outcome=invalid slug=%s",
				rid, userID, workoutID, verr.Slug)
			httpresp.ErrorWithCode(w, http.StatusBadRequest, verr.Msg, verr.Slug)
			return activity.Activity{}, false
		}
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "tcx file exceeds 10 MB limit", "file_too_large")
			return activity.Activity{}, false
		}
		httpresp.ServerError(w, r.Context(), "ingest strength tcx", err)
		return activity.Activity{}, false
	}
}

// resolveDuplicate builds the { kind, id } pointer for a duplicate_activity
// 409: where the file already lives. A live strength_training activity that is
// attached to a live workout resolves to that workout (kind "workout", the
// WORKOUT id). Anything else — a run uploaded via the run importer, or the
// anomalous orphaned strength activity with no live workout — resolves to the
// activity itself (kind "run", the ACTIVITY id), which is a valid id in the
// activities domain the client can navigate to. We never label an activity id
// as kind "workout": the client would GET /workouts/{id} and 404.
func (h *Handler) resolveDuplicate(ctx context.Context, userID string, existing activity.Activity) map[string]any {
	if existing.ActivityType == activity.ActivityStrengthTraining {
		if linked, err := h.repo.GetByActivityID(ctx, userID, existing.ID); err == nil {
			return map[string]any{"kind": "workout", "id": linked.ID}
		}
	}
	return map[string]any{"kind": "run", "id": existing.ID}
}
