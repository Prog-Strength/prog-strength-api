package activity

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
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
	ID string `json:"id"`
	// URL/ThumbURL are null while a photo is still being processed — the
	// objects do not exist yet. The client renders a placeholder at Position
	// rather than a broken image.
	URL      *string `json:"url"`
	ThumbURL *string `json:"thumb_url"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Caption  *string `json:"caption"`
	Position int     `json:"position"`
	// Status is 'ready' or 'processing'. Reads never surface 'pending' or
	// 'failed', so those cannot appear here.
	Status string `json:"status"`
}

// toPhotoDTO hydrates a stored photo into its wire shape, presigning both the
// full and thumbnail variant keys. A presign failure is surfaced as an error so
// the caller can 500 rather than emit a half-formed DTO.
func (h *Handler) toPhotoDTO(ctx context.Context, p ActivityPhoto) (photoDTO, error) {
	dto := photoDTO{
		ID:       p.ID,
		Width:    p.Width,
		Height:   p.Height,
		Caption:  p.Caption,
		Position: p.Position,
		Status:   p.Status,
	}
	// A photo the worker still holds has no objects to presign — its keys are
	// the reservation's placeholders. Presigning those would mint URLs that
	// 404, which reads to the client as a BROKEN photo rather than one that is
	// simply not finished yet.
	if p.Status != PhotoStatusReady {
		return dto, nil
	}

	fullURL, err := h.photoStore.PresignGet(ctx, p.S3Key)
	if err != nil {
		return photoDTO{}, err
	}
	thumbURL, err := h.photoStore.PresignGet(ctx, p.ThumbS3Key)
	if err != nil {
		return photoDTO{}, err
	}
	dto.URL = &fullURL
	dto.ThumbURL = &thumbURL
	return dto, nil
}

// photoStorageReady reports whether the photo write path is wired. When false
// the endpoints degrade to a 503 rather than nil-panic.
func (h *Handler) photoStorageReady() bool {
	return h.photoStore != nil && h.photoRepo != nil
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
	if !isSameIDSet(liveIDs, req.PhotoIDs) {
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

// isSameIDSet reports whether submitted is exactly live as a set: same
// length, no duplicates in submitted, and every submitted id present in live.
func isSameIDSet(live, submitted []string) bool {
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
