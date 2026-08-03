package activity

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// The two-phase photo upload. The client reserves a slot, PUTs the original
// straight to S3, then commits — so the bytes never transit this process and
// no part of the flow is bounded by the server's global 10s request deadline.
//
// See prog-strength-docs/sows/photo-upload-direct-to-s3.md. The synchronous
// POST /activities/{id}/photos stays mounted alongside this until the web
// client has moved.

// --- reserve -----------------------------------------------------------

type reservePhotoRequest struct {
	ContentType string  `json:"content_type"`
	ByteSize    int64   `json:"byte_size"`
	Caption     *string `json:"caption"`
}

type reservePhotoResponse struct {
	PhotoID   string    `json:"photo_id"`
	UploadURL string    `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// reservePhoto handles POST /activities/{id}/photos/reserve — step 1. It
// validates the declared upload, writes a PENDING row, and returns a presigned
// PUT the client uploads to directly. No photo bytes pass through here.
func (h *Handler) reservePhoto(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}
	userID, a, activityID, ok := h.resolveActivityParent(w, r)
	if !ok {
		return
	}

	var req reservePhotoRequest
	// Bounded read: this body is a handful of fields, and the endpoint is
	// reachable before any size check has run.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// The declared content type is a courtesy check only. A presigned PUT
	// accepts whatever the client sends, so the worker re-sniffs the actual
	// bytes before anything is served — this just fails an obviously wrong
	// upload before it starts.
	if _, allowed := uploadExtensions[req.ContentType]; !allowed {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType,
			"photo must be a JPEG, PNG, or WebP image", "unsupported_media_type")
		return
	}
	// Likewise the declared size: a client can lie, so commit HEADs the object
	// and re-checks against the real one.
	if req.ByteSize > h.photoCfg.MaxUploadBytes {
		httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge,
			"photo exceeds the upload size limit", "file_too_large")
		return
	}
	if req.Caption != nil && utf8.RuneCountInString(*req.Caption) > h.photoCfg.CaptionMaxChars {
		httpresp.ErrorWithCode(w, http.StatusBadRequest, "caption is too long", "caption_too_long")
		return
	}

	count, err := h.photoRepo.CountLive(r.Context(), activityID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count live photos", err)
		return
	}
	if count >= h.photoCfg.MaxPerActivity {
		httpresp.ErrorWithCode(w, http.StatusConflict,
			"this activity already has the maximum number of photos", "photo_limit_reached")
		return
	}

	// Reserve the row first: the staging key embeds the photo id, which does
	// not exist until the row does.
	stored, err := h.photoRepo.Insert(r.Context(), ActivityPhoto{
		ActivityID:  activityID,
		UserID:      userID,
		Status:      PhotoStatusPending,
		ContentType: req.ContentType,
		Caption:     req.Caption,
	})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reserve photo", err)
		return
	}

	uploadKey, err := buildPhotoUploadKey(userID, a.ActivityType, a.StartTime, activityID, stored.ID, req.ContentType)
	if err != nil {
		// Most likely an activity type missing from ActivityType.Valid() — see
		// adding-an-activity-type.md. Retire the row so a failed key does not
		// leave a reservation holding a slot against the per-activity cap.
		if delErr := h.photoRepo.SoftDeleteByID(r.Context(), stored.ID); delErr != nil {
			log.Printf("activity photo: retire reservation after key error: photo_id=%s err=%v", stored.ID, delErr)
		}
		httpresp.ServerError(w, r.Context(), "build photo upload key", err)
		return
	}
	if keyErr := h.photoRepo.SetUploadKey(r.Context(), stored.ID, uploadKey); keyErr != nil {
		httpresp.ServerError(w, r.Context(), "set photo upload key", keyErr)
		return
	}

	ttl := time.Duration(h.photoCfg.UploadURLTTLMinutes) * time.Minute
	uploadURL, err := h.photoStore.PresignPut(r.Context(), uploadKey, req.ContentType, ttl)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "presign photo upload", err)
		return
	}

	httpresp.Created(w, "reserved photo upload", reservePhotoResponse{
		PhotoID:   stored.ID,
		UploadURL: uploadURL,
		ExpiresAt: h.now().UTC().Add(ttl),
	})
}

// --- commit ------------------------------------------------------------

// commitPhoto handles POST /activities/{id}/photos/{photo_id}/commit — step 3.
// It confirms the object landed, learns its REAL size, and hands the row to
// the worker. It deliberately does NOT touch the image: that is what keeps
// this handler inside the global 10s budget with orders of magnitude to spare,
// and what stops the timeout this whole design exists to remove from simply
// reappearing here.
func (h *Handler) commitPhoto(w http.ResponseWriter, r *http.Request) {
	if !h.photoStorageReady() {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "photo storage is not configured", "photo_storage_unavailable")
		return
	}
	userID, _, activityID, ok := h.resolveActivityParent(w, r)
	if !ok {
		return
	}
	photoID := chi.URLParam(r, "photo_id")
	if photoID == "" {
		httpresp.Error(w, http.StatusBadRequest, "photo id is required")
		return
	}

	// The reservation is pending, so the ordinary Get (which filters to
	// visible statuses) will not find it — read it by id.
	p, err := h.photoRepo.GetPending(r.Context(), userID, activityID, photoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "photo reservation not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get photo reservation", err)
		return
	}
	if p.UploadS3Key == nil {
		// A reservation whose key was never recorded cannot be committed; the
		// reaper will retire it.
		httpresp.ErrorWithCode(w, http.StatusConflict, "the upload has not completed", "upload_incomplete")
		return
	}

	// Take the size from S3, not from the client. This is the real
	// enforcement point for max_upload_bytes.
	size, err := h.photoStore.Head(r.Context(), *p.UploadS3Key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			httpresp.ErrorWithCode(w, http.StatusConflict, "the upload has not completed", "upload_incomplete")
			return
		}
		httpresp.ServerError(w, r.Context(), "head photo object", err)
		return
	}
	if size > h.photoCfg.MaxUploadBytes {
		// Delete rather than tag: an oversize staged object is still the
		// user's original with its GPS intact, so it should stop existing now
		// rather than at the next lifecycle sweep. Best-effort on both — the
		// row is what the user sees.
		if delErr := h.photoStore.Delete(r.Context(), *p.UploadS3Key); delErr != nil {
			log.Printf("activity photo: delete oversize staged object: key=%s err=%v", *p.UploadS3Key, delErr)
		}
		if delErr := h.photoRepo.SoftDeleteByID(r.Context(), p.ID); delErr != nil {
			log.Printf("activity photo: retire oversize reservation: photo_id=%s err=%v", p.ID, delErr)
		}
		httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge,
			"photo exceeds the upload size limit", "file_too_large")
		return
	}

	if markErr := h.photoRepo.MarkProcessing(r.Context(), userID, activityID, photoID, size); markErr != nil {
		if errors.Is(markErr, ErrNotFound) {
			// Already committed, or retired underneath us. The status guard
			// makes a replayed commit a rejection rather than a second
			// hand-off to the worker.
			httpresp.ErrorWithCode(w, http.StatusConflict, "photo is not awaiting completion", "not_pending")
			return
		}
		httpresp.ServerError(w, r.Context(), "mark photo processing", markErr)
		return
	}

	stored, err := h.photoRepo.Get(r.Context(), userID, activityID, photoID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reload photo", err)
		return
	}
	dto, err := h.toPhotoDTO(r.Context(), stored)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build photo dto", err)
		return
	}
	// 202, not 200: the row exists and is renderable as a placeholder, but the
	// photo itself is not ready yet.
	httpresp.Accepted(w, "committed photo upload", dto)
}
