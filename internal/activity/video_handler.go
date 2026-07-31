package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// videoDTO is the wire shape of one activity video. URL and PosterURL are
// freshly presigned (windowed, cache-stable) GET URLs.
//
// PosterURL is present-as-null when poster generation failed — the client
// renders a placeholder rather than a broken image. Duration/Width/Height are
// the client-reported hints recorded at reservation; they drive the aspect-ratio
// box and the duration badge and are never load-bearing.
type videoDTO struct {
	ID              string   `json:"id"`
	URL             string   `json:"url"`
	PosterURL       *string  `json:"poster_url"`
	ContentType     string   `json:"content_type"`
	ByteSize        int64    `json:"byte_size"`
	DurationSeconds *float64 `json:"duration_seconds"`
	Width           *int     `json:"width"`
	Height          *int     `json:"height"`
	Caption         *string  `json:"caption"`
	Position        int      `json:"position"`
}

// toVideoDTO hydrates a stored video into its wire shape. A poster presign
// failure is NOT fatal: the video is the keepsake and must still be playable,
// so the poster degrades to null and the error is logged.
func (h *Handler) toVideoDTO(ctx context.Context, v ActivityVideo) (videoDTO, error) {
	url, err := h.videoStore.PresignGet(ctx, v.S3Key)
	if err != nil {
		return videoDTO{}, err
	}
	dto := videoDTO{
		ID:              v.ID,
		URL:             url,
		ContentType:     v.ContentType,
		ByteSize:        v.ByteSize,
		DurationSeconds: v.DurationSeconds,
		Width:           v.Width,
		Height:          v.Height,
		Caption:         v.Caption,
		Position:        v.Position,
	}
	if v.PosterS3Key != nil {
		posterURL, err := h.videoStore.PresignGet(ctx, *v.PosterS3Key)
		if err != nil {
			log.Printf("activity video: presign poster failed, degrading to null: video_id=%s err=%v", v.ID, err)
		} else {
			dto.PosterURL = &posterURL
		}
	}
	return dto, nil
}

// videoStorageReady reports whether the video write path is wired. When false
// the endpoints degrade to 503 rather than nil-panic, mirroring photos — which
// is what lets api and infra ship in either order.
func (h *Handler) videoStorageReady() bool {
	return h.videoStore != nil && h.videoRepo != nil
}

// videoStorageUnavailable writes the shared 503.
func videoStorageUnavailable(w http.ResponseWriter) {
	httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "video storage is not configured", "video_storage_unavailable")
}

// resolveVideoParent pulls the authed user and the owned, live parent activity
// off the request. Every video endpoint starts here, so ownership and existence
// are enforced in exactly one place. Returns ok=false with the response already
// written.
func (h *Handler) resolveVideoParent(w http.ResponseWriter, r *http.Request) (userID string, a *Activity, activityID string, ok bool) {
	userID, found := auth.UserIDFrom(r.Context())
	if !found {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return "", nil, "", false
	}
	activityID = chi.URLParam(r, "id")
	if activityID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id is required")
		return "", nil, "", false
	}
	// ErrNotFound covers both a missing activity and one owned by another
	// user — no existence leak, matching the photo surface.
	a, err := h.repo.Get(r.Context(), userID, activityID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return "", nil, "", false
		}
		httpresp.ServerError(w, r.Context(), "get activity", err)
		return "", nil, "", false
	}
	return userID, a, activityID, true
}

// --- reserve -----------------------------------------------------------

type reserveVideoRequest struct {
	ContentType     string   `json:"content_type"`
	ByteSize        int64    `json:"byte_size"`
	DurationSeconds *float64 `json:"duration_seconds"`
	Width           *int     `json:"width"`
	Height          *int     `json:"height"`
	Caption         *string  `json:"caption"`
}

type reserveVideoResponse struct {
	VideoID   string    `json:"video_id"`
	UploadURL string    `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// reserveVideo handles POST /activities/{id}/videos — step 1 of the two-phase
// upload. It validates the declared container and size, enforces the
// per-activity cap, writes a PENDING row, and returns a presigned PUT the
// client uploads to directly. No video bytes pass through this process.
func (h *Handler) reserveVideo(w http.ResponseWriter, r *http.Request) {
	if !h.videoStorageReady() {
		videoStorageUnavailable(w)
		return
	}
	userID, a, activityID, ok := h.resolveVideoParent(w, r)
	if !ok {
		return
	}

	var req reserveVideoRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if !h.videoCfg.allowsContentType(req.ContentType) {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType,
			"unsupported video container; use MP4 or QuickTime", "unsupported_media_type")
		return
	}
	// The declared size is a courtesy check so an obviously-too-large upload
	// fails before it starts. It is NOT the enforcement point — a client can
	// lie here. The commit step HEADs the object and re-checks. See
	// S3VideoStore.PresignPut.
	if req.ByteSize > h.videoCfg.MaxUploadBytes {
		httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge,
			"video exceeds the upload size limit", "file_too_large")
		return
	}
	if req.Caption != nil && utf8.RuneCountInString(*req.Caption) > h.videoCfg.CaptionMaxChars {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "caption is too long", "caption_too_long")
		return
	}

	count, err := h.videoRepo.CountLive(r.Context(), activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count videos", err)
		return
	}
	if count >= h.videoCfg.MaxPerActivity {
		httpresp.ErrorWithCode(w, http.StatusConflict, "this activity already has the maximum number of videos", "video_limit_reached")
		return
	}

	// Reserve the row first so the id exists to key the object on.
	v, err := h.videoRepo.Insert(r.Context(), ActivityVideo{
		ActivityID:      activityID,
		UserID:          userID,
		S3Key:           "", // filled below, once the id is known
		ContentType:     req.ContentType,
		DurationSeconds: req.DurationSeconds,
		Width:           req.Width,
		Height:          req.Height,
		Caption:         req.Caption,
	})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reserve video", err)
		return
	}

	key, err := buildVideoKey(userID, a.ActivityType, a.StartTime, activityID, v.ID, videoVariantVideo, req.ContentType)
	if err != nil {
		// Most likely an activity type missing from ActivityType.Valid() —
		// see adding-an-activity-type.md. Retire the row we just reserved so a
		// failed key doesn't leave a permanent pending slot.
		if delErr := h.videoRepo.SoftDeleteByID(r.Context(), v.ID); delErr != nil {
			log.Printf("activity video: failed to retire reservation after key error: video_id=%s err=%v", v.ID, delErr)
		}
		httpresp.ServerError(w, r.Context(), "build video key", err)
		return
	}
	if setErr := h.videoRepo.SetS3Key(r.Context(), v.ID, key); setErr != nil {
		httpresp.ServerError(w, r.Context(), "set video key", setErr)
		return
	}

	ttl := time.Duration(h.videoCfg.UploadURLTTLMinutes) * time.Minute
	uploadURL, err := h.videoStore.PresignPut(r.Context(), key, req.ContentType, ttl)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "presign video upload", err)
		return
	}

	httpresp.Created(w, "reserved video upload", reserveVideoResponse{
		VideoID:   v.ID,
		UploadURL: uploadURL,
		ExpiresAt: time.Now().UTC().Add(ttl),
	})
}

// --- complete ----------------------------------------------------------

// completeVideo handles POST /activities/{id}/videos/{video_id}/complete —
// step 3. The client posts the poster JPEG as multipart (field "poster",
// optional). The server HEADs the uploaded object to confirm it exists and to
// learn its REAL size, enforces the cap against that, stores the poster through
// the image pipeline, and flips the row to ready.
func (h *Handler) completeVideo(w http.ResponseWriter, r *http.Request) {
	if !h.videoStorageReady() {
		videoStorageUnavailable(w)
		return
	}
	userID, a, activityID, ok := h.resolveVideoParent(w, r)
	if !ok {
		return
	}
	videoID := chi.URLParam(r, "video_id")
	if videoID == "" {
		httpresp.Error(w, http.StatusBadRequest, "video id is required")
		return
	}

	v, err := h.videoRepo.Get(r.Context(), userID, activityID, videoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "video not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get video", err)
		return
	}

	// Confirm the object landed, and take its size from S3 rather than the
	// client. This is the real enforcement point for max_upload_bytes.
	size, err := h.videoStore.Head(r.Context(), v.S3Key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			httpresp.ErrorWithCode(w, http.StatusConflict, "the upload has not completed", "upload_incomplete")
			return
		}
		httpresp.ServerError(w, r.Context(), "head video object", err)
		return
	}
	if size > h.videoCfg.MaxUploadBytes {
		// Tag for lifecycle reaping and retire the reservation. Best-effort on
		// both: the row is what the user sees, and a stray object is collected
		// by the bucket rule.
		if tagErr := h.videoStore.TagOrphaned(r.Context(), v.S3Key); tagErr != nil {
			log.Printf("activity video: tag oversize object failed: key=%s err=%v", v.S3Key, tagErr)
		}
		if delErr := h.videoRepo.SoftDeleteByID(r.Context(), v.ID); delErr != nil {
			log.Printf("activity video: retire oversize reservation failed: video_id=%s err=%v", v.ID, delErr)
		}
		httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge,
			"video exceeds the upload size limit", "file_too_large")
		return
	}

	// The poster is optional and best-effort: a client whose browser couldn't
	// decode the container still gets its video. Never fail the commit over it.
	var posterKey *string
	if formErr := r.ParseMultipartForm(h.photoCfg.MaxUploadBytes); formErr == nil {
		if file, _, ferr := r.FormFile("poster"); ferr == nil {
			defer file.Close()
			if raw, rerr := io.ReadAll(file); rerr == nil && len(raw) > 0 {
				if key, perr := h.storeVideoPoster(r.Context(), userID, a, activityID, v.ID, raw); perr != nil {
					log.Printf("activity video: poster processing failed, continuing without: video_id=%s err=%v", v.ID, perr)
				} else {
					posterKey = &key
				}
			}
		}
	}

	if readyErr := h.videoRepo.MarkReady(r.Context(), userID, activityID, videoID, size, posterKey); readyErr != nil {
		if errors.Is(readyErr, ErrNotFound) {
			// Already committed, or retired underneath us.
			httpresp.ErrorWithCode(w, http.StatusConflict, "video is not awaiting completion", "not_pending")
			return
		}
		httpresp.ServerError(w, r.Context(), "mark video ready", readyErr)
		return
	}

	stored, err := h.videoRepo.Get(r.Context(), userID, activityID, videoID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reload video", err)
		return
	}
	dto, err := h.toVideoDTO(r.Context(), stored)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "presign video", err)
		return
	}
	httpresp.OK(w, "completed video upload", dto)
}

// storeVideoPoster runs the client's poster JPEG through the SAME image
// pipeline photos use — so EXIF (including GPS) is stripped and the dimensions
// are bounded server-side — and writes it under the poster variant key. This is
// the one artifact the server is authoritative over; the video itself is stored
// exactly as uploaded.
func (h *Handler) storeVideoPoster(ctx context.Context, userID string, a *Activity, activityID, videoID string, raw []byte) (string, error) {
	poster, _, err := processPhoto(raw, photoPipelineOpts{
		FullMaxEdge:  h.videoCfg.PosterMaxEdgePx,
		FullQuality:  h.videoCfg.PosterJPEGQuality,
		ThumbMaxEdge: h.videoCfg.PosterMaxEdgePx,
		ThumbQuality: h.videoCfg.PosterJPEGQuality,
	})
	if err != nil {
		return "", err
	}
	key, err := buildVideoKey(userID, a.ActivityType, a.StartTime, activityID, videoID, videoVariantPoster, "")
	if err != nil {
		return "", err
	}
	if err := h.videoStore.Put(ctx, key, "image/jpeg", poster.Bytes); err != nil {
		return "", err
	}
	return key, nil
}

// --- caption / reorder / delete ----------------------------------------

type patchVideoCaptionRequest struct {
	Caption *string `json:"caption"`
}

// patchVideoCaption handles PATCH /activities/{id}/videos/{video_id}.
func (h *Handler) patchVideoCaption(w http.ResponseWriter, r *http.Request) {
	if !h.videoStorageReady() {
		videoStorageUnavailable(w)
		return
	}
	userID, _, activityID, ok := h.resolveVideoParent(w, r)
	if !ok {
		return
	}
	videoID := chi.URLParam(r, "video_id")

	var req patchVideoCaptionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Caption != nil && utf8.RuneCountInString(*req.Caption) > h.videoCfg.CaptionMaxChars {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "caption is too long", "caption_too_long")
		return
	}

	if err := h.videoRepo.UpdateCaption(r.Context(), userID, activityID, videoID, req.Caption); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "video not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update video caption", err)
		return
	}
	h.respondVideoList(w, r, userID, activityID)
}

type reorderVideosRequest struct {
	VideoIDs []string `json:"video_ids"`
}

// reorderVideos handles PUT /activities/{id}/videos/order. The body must be
// EXACTLY the current live ready set — same elements, once each.
func (h *Handler) reorderVideos(w http.ResponseWriter, r *http.Request) {
	if !h.videoStorageReady() {
		videoStorageUnavailable(w)
		return
	}
	userID, _, activityID, ok := h.resolveVideoParent(w, r)
	if !ok {
		return
	}

	var req reorderVideosRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	liveIDs, err := h.videoRepo.LiveIDs(r.Context(), userID, activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list live video ids", err)
		return
	}
	if !isSameIDSet(liveIDs, req.VideoIDs) {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "video_ids must list exactly the activity's videos, once each", "invalid_video_order")
		return
	}
	if err := h.videoRepo.Reorder(r.Context(), userID, activityID, req.VideoIDs); err != nil {
		httpresp.ServerError(w, r.Context(), "reorder videos", err)
		return
	}
	h.respondVideoList(w, r, userID, activityID)
}

// deleteVideo handles DELETE /activities/{id}/videos/{video_id}. The row is
// soft-deleted and both objects are best-effort tagged for lifecycle reaping.
func (h *Handler) deleteVideo(w http.ResponseWriter, r *http.Request) {
	if !h.videoStorageReady() {
		videoStorageUnavailable(w)
		return
	}
	userID, _, activityID, ok := h.resolveVideoParent(w, r)
	if !ok {
		return
	}
	videoID := chi.URLParam(r, "video_id")

	v, err := h.videoRepo.Get(r.Context(), userID, activityID, videoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "video not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get video", err)
		return
	}
	if err := h.videoRepo.SoftDelete(r.Context(), userID, activityID, videoID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "video not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete video", err)
		return
	}
	// Best-effort: the row is gone from the user's view either way.
	if err := h.videoStore.TagOrphaned(r.Context(), v.S3Key); err != nil {
		log.Printf("activity video: tag deleted object failed: key=%s err=%v", v.S3Key, err)
	}
	if v.PosterS3Key != nil {
		if err := h.videoStore.TagOrphaned(r.Context(), *v.PosterS3Key); err != nil {
			log.Printf("activity video: tag deleted poster failed: key=%s err=%v", *v.PosterS3Key, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// respondVideoList writes the activity's current ready videos, the shared
// success shape for caption and reorder.
func (h *Handler) respondVideoList(w http.ResponseWriter, r *http.Request, userID, activityID string) {
	videos, err := h.videoRepo.ListByActivity(r.Context(), userID, activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list videos", err)
		return
	}
	dtos := make([]videoDTO, 0, len(videos))
	for _, v := range videos {
		dto, err := h.toVideoDTO(r.Context(), v)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "presign video", err)
			return
		}
		dtos = append(dtos, dto)
	}
	httpresp.OK(w, "videos", dtos)
}

// --- detail-read attachment --------------------------------------------

// attachVideos fills the detail DTO's videos array. Gated on the DATA (storage
// wired, rows present) and NEVER on a.ActivityType — every activity type that
// can hold a video shows one, which is the requirement the photo rollout
// missed. See sows/activity-videos.md § Route coverage.
func (h *Handler) attachVideos(ctx context.Context, userID, activityID string, dto *activityDTO) error {
	if !h.videoStorageReady() {
		return nil
	}
	videos, err := h.videoRepo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		return err
	}
	dtos := make([]videoDTO, 0, len(videos))
	for _, v := range videos {
		vd, err := h.toVideoDTO(ctx, v)
		if err != nil {
			return err
		}
		dtos = append(dtos, vd)
	}
	dto.Videos = &dtos
	return nil
}

// ReapStalePendingVideos retires reservations whose client never completed the
// upload: it tags any object that did land for lifecycle collection and
// soft-deletes the row. Safe to call repeatedly; a no-op when video storage is
// unwired. Returns the number of rows retired.
//
// Deliberately a plain method rather than a background goroutine — the caller
// (server wiring) decides the cadence, and at single-user volume a startup
// sweep is enough.
func (h *Handler) ReapStalePendingVideos(ctx context.Context) (int, error) {
	if !h.videoStorageReady() {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(h.videoCfg.PendingReapAfterHours) * time.Hour)
	stale, err := h.videoRepo.ListStalePending(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, v := range stale {
		if v.S3Key != "" {
			if err := h.videoStore.TagOrphaned(ctx, v.S3Key); err != nil {
				log.Printf("activity video: reaper tag failed: key=%s err=%v", v.S3Key, err)
			}
		}
		if err := h.videoRepo.SoftDeleteByID(ctx, v.ID); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}
