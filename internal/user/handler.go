package user

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// maxAvatarBytes caps the avatar multipart upload. 2 MB is generous for a
// profile picture while bounding per-request memory (the whole file is read
// into a byte slice to sniff its type and write it to S3).
const maxAvatarBytes = 2 << 20

// Handler serves the authed user's own account at /me. GET reads the resolved
// profile; PATCH is the preferences/profile write path; POST/DELETE /me/avatar
// manage the uploaded avatar. The frontend needs the resolved profile (notably
// weight_unit/distance_unit and the resolved avatar_url) to render user-scoped
// views without threading preferences through every request.
type Handler struct {
	repo Repository
	// store is the avatar object store. It may be nil when no avatar bucket is
	// configured — GET /me and PATCH /me still work (no presign attempted);
	// the avatar upload/delete endpoints return 503.
	store AvatarStore
}

// NewHandler constructs the user handler. store may be nil when the deployment
// has no avatar bucket configured (avatar endpoints then return 503; profile
// reads/writes are unaffected).
func NewHandler(repo Repository, store AvatarStore) *Handler {
	return &Handler{repo: repo, store: store}
}

// Mount registers routes on the given router. The router is expected to be
// the JWT-gated group (wrapped in auth.RequireUser) — the handlers read the
// user ID out of request context, which the middleware populates.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/me", h.getMe)
	r.Patch("/me", h.updateMe)
	r.Post("/me/avatar", h.uploadAvatar)
	r.Delete("/me/avatar", h.deleteAvatar)
}

// meResponse is the resolved profile shape returned by every /me endpoint, so
// the client always gets the same DTO. avatar_url is a freshly presigned S3 GET
// (when an avatar is uploaded), the OAuth avatar URL fallback, or null.
type meResponse struct {
	ID                    string   `json:"id"`
	Email                 string   `json:"email"`
	DisplayName           string   `json:"display_name"`
	WeightUnit            string   `json:"weight_unit"`
	DistanceUnit          string   `json:"distance_unit"`
	HeightCm              *float64 `json:"height_cm"`
	Timezone              string   `json:"timezone"`
	CalendarDefaultDetail string   `json:"calendar_default_detail"`
	AvatarURL             *string  `json:"avatar_url"`
}

// resolveMe builds the resolved profile DTO for a user. The avatar_url is, in
// priority order: a presigned GET of the uploaded avatar, the OAuth avatar
// fallback, or null. A presign failure is logged and degrades to the OAuth
// fallback (or null) rather than failing the whole profile response.
func (h *Handler) resolveMe(r *http.Request, u *User) meResponse {
	resp := meResponse{
		ID:                    u.ID,
		Email:                 u.Email,
		DisplayName:           u.DisplayName,
		WeightUnit:            string(u.WeightUnit),
		DistanceUnit:          string(u.DistanceUnit),
		HeightCm:              u.HeightCm,
		Timezone:              u.Timezone,
		CalendarDefaultDetail: u.CalendarDefaultDetail,
	}
	switch {
	case u.AvatarKey != nil && h.store != nil:
		url, err := h.store.PresignGet(r.Context(), *u.AvatarKey)
		if err != nil {
			log.Printf("avatar presign: user_id=%s key=%s err=%v", u.ID, *u.AvatarKey, err)
			resp.AvatarURL = u.OAuthAvatarURL // graceful fallback
		} else {
			resp.AvatarURL = &url
		}
	case u.OAuthAvatarURL != nil:
		resp.AvatarURL = u.OAuthAvatarURL
	}
	return resp
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	u, ok := h.loadUser(w, r)
	if !ok {
		return
	}
	httpresp.OK(w, "got user", h.resolveMe(r, u))
}

// updateMeRequest is the PATCH /me body. Fields are pointers so absence (nil)
// is distinguishable from a zero value — only provided fields are applied,
// making the update additive/partial. This endpoint deliberately does NOT
// touch avatar_key; avatar mutation goes through POST/DELETE /me/avatar.
type updateMeRequest struct {
	DisplayName           *string       `json:"display_name"`
	WeightUnit            *WeightUnit   `json:"weight_unit"`
	DistanceUnit          *DistanceUnit `json:"distance_unit"`
	HeightCm              *float64      `json:"height_cm"`
	Timezone              *string       `json:"timezone"`
	CalendarDefaultDetail *string       `json:"calendar_default_detail"`
}

// updateMe is the preferences/profile write path. It loads the current user,
// applies only the fields present in the request body (additive/partial
// update), validates, persists, and returns the freshly resolved profile.
// Email is immutable through this path (handled by the repository).
func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	var req updateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	u, ok := h.loadUser(w, r)
	if !ok {
		return
	}

	// Apply only provided fields, leaving the rest untouched. height_cm uses
	// pointer-presence: nil means "absent" (height is never cleared via PATCH;
	// the SOW/plan only require setting it). A provided value (including an
	// out-of-range one) is validated below.
	if req.DisplayName != nil {
		u.DisplayName = *req.DisplayName
	}
	if req.WeightUnit != nil {
		u.WeightUnit = *req.WeightUnit
	}
	if req.DistanceUnit != nil {
		u.DistanceUnit = *req.DistanceUnit
	}
	if req.HeightCm != nil {
		u.HeightCm = req.HeightCm
	}
	if req.Timezone != nil {
		u.Timezone = *req.Timezone
	}
	if req.CalendarDefaultDetail != nil {
		u.CalendarDefaultDetail = *req.CalendarDefaultDetail
	}

	// Validate at the boundary: a blank/over-long display name, an unknown
	// enum, an out-of-range height, an invalid timezone, or a bad calendar
	// detail is a client error (400), not a 500.
	if err := u.Validate(); err != nil {
		var enumErr *InvalidEnumError
		if errors.Is(err, ErrDisplayNameRequired) ||
			errors.Is(err, ErrDisplayNameTooLong) ||
			errors.Is(err, ErrHeightOutOfRange) ||
			errors.Is(err, ErrInvalidTimezone) ||
			errors.Is(err, ErrInvalidCalendarDetail) ||
			errors.As(err, &enumErr) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "validate user", err)
		return
	}

	if err := h.repo.Update(r.Context(), u); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update user", err)
		return
	}

	httpresp.OK(w, "updated user", h.resolveMe(r, u))
}

// uploadAvatar handles POST /me/avatar: a multipart upload of an image under
// the "file" field. It caps the size (413), sniffs the content type against an
// allowlist (415), writes a fresh-keyed S3 object, updates avatar_key, and
// best-effort tags the previous object for lifecycle reaping.
func (h *Handler) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "avatar storage is not configured", "avatar_storage_unavailable")
		return
	}

	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	// Cap the body before reading. MaxBytesReader makes the read error out
	// once the cap is exceeded, so an oversized upload can't exhaust memory.
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "avatar exceeds 2 MB limit", "file_too_large")
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

	// MaxBytesReader can also fire here from the io.ReadAll over the part body.
	body, err := io.ReadAll(file)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httpresp.ErrorWithCode(w, http.StatusRequestEntityTooLarge, "avatar exceeds 2 MB limit", "file_too_large")
			return
		}
		httpresp.ServerError(w, r.Context(), "read avatar upload", err)
		return
	}

	// Sniff the content type (don't trust the client header) and require it
	// in the allowlist.
	contentType := http.DetectContentType(body)
	ext, ok := ExtForContentType(contentType)
	if !ok {
		httpresp.ErrorWithCode(w, http.StatusUnsupportedMediaType, "avatar must be a PNG, JPEG, or WebP image", "unsupported_media_type")
		return
	}

	key := AvatarKey(userID, ext)
	if putErr := h.store.Put(r.Context(), key, contentType, body); putErr != nil {
		httpresp.ServerError(w, r.Context(), "put avatar", putErr)
		return
	}

	u, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user", err)
		return
	}

	prevKey := u.AvatarKey
	u.AvatarKey = &key
	if err := h.repo.Update(r.Context(), u); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update user", err)
		return
	}

	// Best-effort: tag the superseded object so the lifecycle rule reaps it.
	// Off the hot path and failure-tolerant — a tag failure must not fail the
	// upload (the row already points at the new object).
	h.tagOrphaned(r, userID, prevKey)

	httpresp.OK(w, "uploaded avatar", h.resolveMe(r, u))
}

// deleteAvatar handles DELETE /me/avatar: it clears avatar_key (reverting the
// resolved avatar to the OAuth fallback or null) and best-effort tags the old
// object for lifecycle reaping.
func (h *Handler) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpresp.ErrorWithCode(w, http.StatusServiceUnavailable, "avatar storage is not configured", "avatar_storage_unavailable")
		return
	}

	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	u, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user", err)
		return
	}

	prevKey := u.AvatarKey
	u.AvatarKey = nil
	if err := h.repo.Update(r.Context(), u); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update user", err)
		return
	}

	h.tagOrphaned(r, userID, prevKey)

	httpresp.OK(w, "deleted avatar", h.resolveMe(r, u))
}

// tagOrphaned best-effort tags a (possibly nil) previous avatar key. A nil key
// (no previous avatar) is a no-op; tagging errors are logged, not surfaced.
func (h *Handler) tagOrphaned(r *http.Request, userID string, prevKey *string) {
	if prevKey == nil || h.store == nil {
		return
	}
	if err := h.store.TagOrphaned(r.Context(), *prevKey); err != nil {
		log.Printf("avatar tag orphaned: user_id=%s key=%s err=%v", userID, *prevKey, err)
	}
}

// loadUser reads the user ID from context and fetches the row, writing the
// appropriate error response and returning ok=false on any failure.
func (h *Handler) loadUser(w http.ResponseWriter, r *http.Request) (*User, bool) {
	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return nil, false
	}
	u, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return nil, false
		}
		httpresp.ServerError(w, r.Context(), "get user", err)
		return nil, false
	}
	return u, true
}
