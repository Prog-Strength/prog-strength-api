package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// allowedPhotoContentTypes is the sniffed-content-type allowlist for a photo
// upload. The client's declared type is ignored; http.DetectContentType decides.
var allowedPhotoContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// photoDTO is the wire shape of one activity photo. URL/ThumbURL are freshly
// presigned (windowed, cache-stable) GET URLs for the full and thumb variants.
// Caption is present-as-null.
type photoDTO struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	ThumbURL string  `json:"thumb_url"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Caption  *string `json:"caption"`
	Position int     `json:"position"`
}

// toPhotoDTO hydrates a stored photo into its wire shape, presigning both the
// full and thumbnail variant keys. A presign failure is surfaced as an error so
// the caller can 500 rather than emit a half-formed DTO.
func (h *Handler) toPhotoDTO(ctx context.Context, p ActivityPhoto) (photoDTO, error) {
	fullURL, err := h.photoStore.PresignGet(ctx, p.S3Key)
	if err != nil {
		return photoDTO{}, err
	}
	thumbURL, err := h.photoStore.PresignGet(ctx, p.ThumbS3Key)
	if err != nil {
		return photoDTO{}, err
	}
	return photoDTO{
		ID:       p.ID,
		URL:      fullURL,
		ThumbURL: thumbURL,
		Width:    p.Width,
		Height:   p.Height,
		Caption:  p.Caption,
		Position: p.Position,
	}, nil
}

// photoStorageReady reports whether the photo write path is wired. When false
// the endpoints degrade to a 503 rather than nil-panic.
func (h *Handler) photoStorageReady() bool {
	return h.photoStore != nil && h.photoRepo != nil
}

// uploadPhoto handles POST /activities/{id}/photos: a multipart upload of an
// image under the "photo" field with an optional "caption" form field. It caps
// the size (413), sniffs the content type against an allowlist (415), enforces
// the per-activity limit (409), re-encodes two JPEG variants (stripping EXIF),
// writes both to object storage, and records the row.
func (h *Handler) uploadPhoto(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}

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

	// Resolve the owned, live parent activity. ErrNotFound covers both a
	// missing activity and one owned by another user — no existence leak.
	a, getErr := h.repo.Get(r.Context(), userID, activityID)
	if getErr != nil {
		if errors.Is(getErr, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get activity", getErr)
		return
	}

	// Cap the body before reading. MaxBytesReader makes the read error out once
	// the cap is exceeded, so an oversized upload can't exhaust memory.
	r.Body = http.MaxBytesReader(w, r.Body, h.photoCfg.MaxUploadBytes)
	if err := r.ParseMultipartForm(h.photoCfg.MaxUploadBytes); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "photo exceeds the upload size limit", "file_too_large")
			return
		}
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "expected a multipart upload with a photo field", "unsupported_media_type")
		return
	}

	file, _, err := r.FormFile("photo")
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "missing photo field in multipart upload", "unsupported_media_type")
		return
	}
	defer file.Close()

	// MaxBytesReader can also fire here from the io.ReadAll over the part body.
	body, err := io.ReadAll(file)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "photo exceeds the upload size limit", "file_too_large")
			return
		}
		httpresp.ServerError(w, r.Context(), "read photo upload", err)
		return
	}

	// Sniff the content type (don't trust the client header) and require it in
	// the allowlist.
	contentType := http.DetectContentType(body)
	if !allowedPhotoContentTypes[contentType] {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "photo must be a JPEG, PNG, or WebP image", "unsupported_media_type")
		return
	}

	// Optional caption. Enforce the length cap up front.
	var caption *string
	if raw := r.FormValue("caption"); raw != "" {
		if utf8.RuneCountInString(raw) > h.photoCfg.CaptionMaxChars {
			httpresp.ErrorWithCode(w, http.StatusBadRequest, "caption is too long", "caption_too_long")
			return
		}
		caption = &raw
	}

	// Enforce the per-activity photo limit against the live count.
	count, err := h.photoRepo.CountLive(r.Context(), activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count live photos", err)
		return
	}
	if count >= h.photoCfg.MaxPerActivity {
		httpresp.ErrorWithCode(w, http.StatusConflict, "this activity already has the maximum number of photos", "photo_limit_reached")
		return
	}

	// Re-encode the two JPEG variants (EXIF stripped by the pipeline). A decode
	// failure means the bytes weren't a usable image → 415.
	full, thumb, err := processPhoto(body, photoPipelineOpts{
		FullMaxEdge:  h.photoCfg.FullMaxEdgePx,
		FullQuality:  h.photoCfg.FullJPEGQuality,
		ThumbMaxEdge: h.photoCfg.ThumbMaxEdgePx,
		ThumbQuality: h.photoCfg.ThumbJPEGQuality,
	})
	if err != nil {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "photo could not be processed as an image", "unsupported_media_type")
		return
	}

	photoID := id.New()
	fullKey, err := buildPhotoKey(userID, a.ActivityType, a.StartTime, activityID, photoID, photoVariantFull)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build full photo key", err)
		return
	}
	thumbKey, err := buildPhotoKey(userID, a.ActivityType, a.StartTime, activityID, photoID, photoVariantThumb)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build thumb photo key", err)
		return
	}

	// Put the full variant first, then the thumb. Both are stored as JPEG.
	if err = h.photoStore.Put(r.Context(), fullKey, "image/jpeg", full.Bytes); err != nil {
		// Nothing durable exists yet — no object to orphan.
		httpresp.ServerError(w, r.Context(), "put full photo", err)
		return
	}
	if err = h.photoStore.Put(r.Context(), thumbKey, "image/jpeg", thumb.Bytes); err != nil {
		// The full object is orphaned: best-effort tag it for lifecycle reaping
		// so we don't leak it, and write NO row.
		if tagErr := h.photoStore.TagOrphaned(r.Context(), fullKey); tagErr != nil {
			log.Printf("activity photo: tag orphaned full key after thumb put failure: key=%s err=%v", fullKey, tagErr)
		}
		httpresp.ServerError(w, r.Context(), "put thumb photo", err)
		return
	}

	now := h.now().UTC()
	stored, err := h.photoRepo.Insert(r.Context(), ActivityPhoto{
		ActivityID:  activityID,
		UserID:      userID,
		S3Key:       fullKey,
		ThumbS3Key:  thumbKey,
		ContentType: "image/jpeg",
		ByteSize:    int64(len(full.Bytes)),
		Width:       full.Width,
		Height:      full.Height,
		Caption:     caption,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "insert photo", err)
		return
	}

	dto, err := h.toPhotoDTO(r.Context(), stored)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "presign photo", err)
		return
	}
	httpresp.Created(w, "uploaded photo", dto)
}

// patchPhotoCaption handles PATCH /activities/{id}/photos/{photo_id}: set or
// clear a photo's caption. Body is {"caption": string|null}.
func (h *Handler) patchPhotoCaption(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}

	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	activityID := chi.URLParam(r, "id")
	photoID := chi.URLParam(r, "photo_id")
	if activityID == "" || photoID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id and photo id are required")
		return
	}

	var req struct {
		Caption *string `json:"caption"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Caption != nil && utf8.RuneCountInString(*req.Caption) > h.photoCfg.CaptionMaxChars {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "caption is too long", "caption_too_long")
		return
	}

	if err := h.photoRepo.UpdateCaption(r.Context(), userID, activityID, photoID, req.Caption); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "photo not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update caption", err)
		return
	}

	// Re-read the row so the response reflects the persisted state.
	p, err := h.photoRepo.Get(r.Context(), userID, activityID, photoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "photo not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get photo", err)
		return
	}
	dto, err := h.toPhotoDTO(r.Context(), p)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "presign photo", err)
		return
	}
	httpresp.OK(w, "updated photo caption", dto)
}

// reorderPhotos handles PUT /activities/{id}/photos/order: rewrite the display
// order. Body is {"photo_ids": [...]} and must be EXACTLY the current live set
// (same elements, no dups, no missing, no extras).
func (h *Handler) reorderPhotos(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}

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
		PhotoIDs []string `json:"photo_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	liveIDs, err := h.photoRepo.LiveIDs(r.Context(), userID, activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list live photo ids", err)
		return
	}

	// The submitted list must be a permutation of the live set: same length,
	// every submitted id live, and no duplicates.
	if !isSamePhotoSet(liveIDs, req.PhotoIDs) {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "photo_ids must list exactly the activity's photos, once each", "invalid_photo_order")
		return
	}

	if err = h.photoRepo.Reorder(r.Context(), userID, activityID, req.PhotoIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "activity not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "reorder photos", err)
		return
	}

	photos, err := h.photoRepo.ListByActivity(r.Context(), userID, activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list photos", err)
		return
	}
	dtos := make([]photoDTO, 0, len(photos))
	for _, p := range photos {
		dto, err := h.toPhotoDTO(r.Context(), p)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "presign photo", err)
			return
		}
		dtos = append(dtos, dto)
	}
	httpresp.OK(w, "reordered photos", dtos)
}

// isSamePhotoSet reports whether submitted is exactly live as a set: same
// length, no duplicates in submitted, and every submitted id present in live.
func isSamePhotoSet(live, submitted []string) bool {
	if len(live) != len(submitted) {
		return false
	}
	liveSet := make(map[string]struct{}, len(live))
	for _, pid := range live {
		liveSet[pid] = struct{}{}
	}
	seen := make(map[string]struct{}, len(submitted))
	for _, pid := range submitted {
		if _, ok := liveSet[pid]; !ok {
			return false // extra / unknown id
		}
		if _, dup := seen[pid]; dup {
			return false // duplicate
		}
		seen[pid] = struct{}{}
	}
	// Equal length + every submitted in live + no dups ⇒ exact match.
	return true
}

// deletePhoto handles DELETE /activities/{id}/photos/{photo_id}: soft-delete a
// photo and best-effort tag both its object variants for lifecycle reaping.
func (h *Handler) deletePhoto(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}

	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	activityID := chi.URLParam(r, "id")
	photoID := chi.URLParam(r, "photo_id")
	if activityID == "" || photoID == "" {
		httpresp.Error(w, http.StatusBadRequest, "activity id and photo id are required")
		return
	}

	// Load the row first so we have its keys to orphan after the soft-delete.
	p, err := h.photoRepo.Get(r.Context(), userID, activityID, photoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "photo not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get photo", err)
		return
	}

	if err := h.photoRepo.SoftDelete(r.Context(), userID, activityID, photoID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "photo not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete photo", err)
		return
	}

	// Best-effort: tag both variants so the lifecycle rule reaps them. Off the
	// hot path and failure-tolerant — a tag failure must not fail the delete.
	for _, key := range []string{p.S3Key, p.ThumbS3Key} {
		if err := h.photoStore.TagOrphaned(r.Context(), key); err != nil {
			log.Printf("activity photo: tag orphaned on delete: key=%s err=%v", key, err)
		}
	}

	httpresp.OK(w, "deleted photo", map[string]bool{"deleted": true})
}
