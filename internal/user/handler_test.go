package user

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// envelope mirrors the success shape from httpresp, with Data typed as a
// *User so we can assert on the returned account directly.
type envelope struct {
	Message string `json:"message"`
	Data    *User  `json:"data"`
}

func TestGetMe_ReturnsUser(t *testing.T) {
	repo := NewMemoryRepository()
	u := &User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()

	NewHandler(repo, NewFakeAvatarStore()).getMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil {
		t.Fatalf("data is nil, body=%s", w.Body.String())
	}
	if got.Data.ID != u.ID || got.Data.Email != "lifter@example.com" || got.Data.WeightUnit != WeightUnitPounds {
		t.Fatalf("unexpected user: %+v", got.Data)
	}
	if got.Data.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("distance_unit: got %q want %q", got.Data.DistanceUnit, DistanceUnitMiles)
	}
}

// seedUser is a small helper to insert a fully-valid user for handler tests.
func seedUser(t *testing.T, repo Repository) *User {
	t.Helper()
	u := &User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// patchMe drives the updateMe handler with the given JSON body for the user.
func patchMe(repo Repository, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PATCH", "/me", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	NewHandler(repo, NewFakeAvatarStore()).updateMe(w, req)
	return w
}

func TestUpdateMe_UpdatesDistanceUnit(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"distance_unit":"km"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil || got.Data.DistanceUnit != DistanceUnitKilometers {
		t.Fatalf("distance_unit not updated: %+v", got.Data)
	}
	// Other prefs untouched.
	if got.Data.WeightUnit != WeightUnitPounds || got.Data.DisplayName != "Lifter" {
		t.Fatalf("unexpected mutation: %+v", got.Data)
	}
}

func TestUpdateMe_InvalidDistanceUnit(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"distance_unit":"furlongs"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}

	// Persisted value should be unchanged.
	after, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if after.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("distance_unit mutated on invalid update: %q", after.DistanceUnit)
	}
}

func TestUpdateMe_DisplayNameOnlyLeavesUnitsUnchanged(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"display_name":"New Name"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil {
		t.Fatalf("data is nil, body=%s", w.Body.String())
	}
	if got.Data.DisplayName != "New Name" {
		t.Fatalf("display_name not updated: %+v", got.Data)
	}
	if got.Data.WeightUnit != WeightUnitPounds || got.Data.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("units changed unexpectedly: %+v", got.Data)
	}
}

func TestUpdateMe_SetsUsernameCanonicalized(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"username":"@JimLifts"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil || got.Data.Username == nil || *got.Data.Username != "jimlifts" {
		t.Fatalf("username not canonicalized: %+v", got.Data)
	}
}

func TestUpdateMe_InvalidUsername(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"username":"jim-lifts"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	after, _ := repo.GetByID(context.Background(), u.ID)
	if after.Username != nil {
		t.Fatalf("username should not be set on invalid update: %v", *after.Username)
	}
}

func TestUpdateMe_ReservedUsername(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"username":"admin"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateMe_UsernameTakenConflict(t *testing.T) {
	repo := NewMemoryRepository()
	a := seedUser(t, repo)
	b := &User{Email: "b@example.com", DisplayName: "B", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
	if err := repo.Create(context.Background(), b); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	if w := patchMe(repo, a.ID, `{"username":"taken"}`); w.Code != http.StatusOK {
		t.Fatalf("set a username: got %d, body=%s", w.Code, w.Body.String())
	}
	// B tries the case-variant of A's handle -> 409.
	w := patchMe(repo, b.ID, `{"username":"TAKEN"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409, body=%s", w.Code, w.Body.String())
	}
}

func TestGetMe_NotFound(t *testing.T) {
	repo := NewMemoryRepository()

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "nonexistent"))
	w := httptest.NewRecorder()

	NewHandler(repo, NewFakeAvatarStore()).getMe(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
	}
}

// meEnvelope decodes the resolved profile DTO returned by all /me endpoints.
type meEnvelope struct {
	Message string     `json:"message"`
	Data    meResponse `json:"data"`
}

func ptrString(s string) *string  { return &s }
func ptrFloat(f float64) *float64 { return &f }

func decodeMe(t *testing.T, w *httptest.ResponseRecorder) meResponse {
	t.Helper()
	var env meEnvelope
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode me: %v (body=%s)", err, w.Body.String())
	}
	return env.Data
}

// --- A5: GET /me resolved avatar_url -------------------------------------

func TestGetMe_AvatarKeyPresignsURL(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	u := seedUser(t, repo)
	u.AvatarKey = ptrString("user_id=u1/x.png")
	if err := repo.Update(context.Background(), u); err != nil {
		t.Fatalf("update: %v", err)
	}

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()
	NewHandler(repo, store).getMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeMe(t, w)
	want := "https://signed.example/user_id=u1/x.png"
	if got.AvatarURL == nil || *got.AvatarURL != want {
		t.Fatalf("avatar_url: got %v want %q", got.AvatarURL, want)
	}
}

func TestGetMe_OAuthFallbackWhenNoAvatarKey(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	u := seedUser(t, repo)
	u.OAuthAvatarURL = ptrString("https://oauth.example/pic.png")
	u.HeightCm = ptrFloat(182)
	if err := repo.Update(context.Background(), u); err != nil {
		t.Fatalf("update: %v", err)
	}

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()
	NewHandler(repo, store).getMe(w, req)

	got := decodeMe(t, w)
	if got.AvatarURL == nil || *got.AvatarURL != "https://oauth.example/pic.png" {
		t.Fatalf("avatar_url: got %v want oauth url", got.AvatarURL)
	}
	if got.HeightCm == nil || *got.HeightCm != 182 {
		t.Fatalf("height_cm passthrough: got %v want 182", got.HeightCm)
	}
}

func TestGetMe_AvatarNullWhenNeitherSet(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	u := seedUser(t, repo)

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()
	NewHandler(repo, store).getMe(w, req)

	got := decodeMe(t, w)
	if got.AvatarURL != nil {
		t.Fatalf("avatar_url: got %v want nil", *got.AvatarURL)
	}
}

// --- A6: PATCH /me height + validation -----------------------------------

func TestUpdateMe_SetsHeight(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"height_cm":180}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeMe(t, w)
	if got.HeightCm == nil || *got.HeightCm != 180 {
		t.Fatalf("height_cm echo: got %v want 180", got.HeightCm)
	}
	// Persisted.
	after, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.HeightCm == nil || *after.HeightCm != 180 {
		t.Fatalf("height_cm persisted: got %v want 180", after.HeightCm)
	}
}

func TestUpdateMe_EmptyNameRejected(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)
	w := patchMe(repo, u.ID, `{"display_name":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateMe_OverlongNameRejected(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)
	body := `{"display_name":"` + strings.Repeat("a", 61) + `"}`
	w := patchMe(repo, u.ID, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateMe_OutOfRangeHeightRejected(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)
	w := patchMe(repo, u.ID, `{"height_cm":300}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	// Unchanged on rejection.
	after, _ := repo.GetByID(context.Background(), u.ID)
	if after.HeightCm != nil {
		t.Fatalf("height mutated on invalid update: %v", *after.HeightCm)
	}
}

// --- A7: POST/DELETE /me/avatar ------------------------------------------

// pngBytes returns a minimal byte slice that http.DetectContentType sniffs as
// image/png (the 8-byte PNG signature is enough).
func pngBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
}

// avatarMultipart builds a multipart body with data under the "file" field.
func avatarMultipart(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "avatar.bin")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func doUpload(t *testing.T, h *Handler, userID string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := avatarMultipart(t, data)
	req := httptest.NewRequest("POST", "/me/avatar", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	h.uploadAvatar(w, req)
	return w
}

func TestUploadAvatar_ValidPNG(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	h := NewHandler(repo, store)
	u := seedUser(t, repo)

	w := doUpload(t, h, u.ID, pngBytes())
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	got := decodeMe(t, w)
	if got.AvatarURL == nil || !strings.HasPrefix(*got.AvatarURL, "https://signed.example/user_id="+u.ID+"/") {
		t.Fatalf("avatar_url not presigned sentinel: %v", got.AvatarURL)
	}
	// avatar_key persisted on the row.
	after, _ := repo.GetByID(context.Background(), u.ID)
	if after.AvatarKey == nil || !strings.HasPrefix(*after.AvatarKey, "user_id="+u.ID+"/") {
		t.Fatalf("avatar_key not set: %v", after.AvatarKey)
	}
}

func TestUploadAvatar_TagsPreviousKey(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	h := NewHandler(repo, store)
	u := seedUser(t, repo)

	// First upload: no previous key, must NOT tag.
	doUpload(t, h, u.ID, pngBytes())
	if store.tagCallCount() != 0 {
		t.Fatalf("first upload tagged with no previous key: calls=%d", store.tagCallCount())
	}
	after, _ := repo.GetByID(context.Background(), u.ID)
	firstKey := *after.AvatarKey

	// Second upload: previous key must be tagged orphaned.
	doUpload(t, h, u.ID, pngBytes())
	if !store.wasTagged(firstKey) {
		t.Fatalf("previous key %q not tagged orphaned", firstKey)
	}
}

func TestUploadAvatar_OversizedRejected(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	h := NewHandler(repo, store)
	u := seedUser(t, repo)

	big := make([]byte, maxAvatarBytes+1)
	copy(big, pngBytes())
	w := doUpload(t, h, u.ID, big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d want 413, body=%s", w.Code, w.Body.String())
	}
}

func TestUploadAvatar_UnsupportedTypeRejected(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	h := NewHandler(repo, store)
	u := seedUser(t, repo)

	w := doUpload(t, h, u.ID, []byte("just some plain text content here"))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status: got %d want 415, body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteAvatar_ClearsAndTagsAndReturnsFallback(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewFakeAvatarStore()
	h := NewHandler(repo, store)
	u := seedUser(t, repo)
	u.OAuthAvatarURL = ptrString("https://oauth.example/pic.png")
	if err := repo.Update(context.Background(), u); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Upload first so there's a key to delete.
	doUpload(t, h, u.ID, pngBytes())
	after, _ := repo.GetByID(context.Background(), u.ID)
	uploadedKey := *after.AvatarKey

	req := httptest.NewRequest("DELETE", "/me/avatar", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()
	h.deleteAvatar(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	got := decodeMe(t, w)
	if got.AvatarURL == nil || *got.AvatarURL != "https://oauth.example/pic.png" {
		t.Fatalf("avatar_url should fall back to oauth: got %v", got.AvatarURL)
	}
	if !store.wasTagged(uploadedKey) {
		t.Fatalf("deleted key %q not tagged orphaned", uploadedKey)
	}
	cleared, _ := repo.GetByID(context.Background(), u.ID)
	if cleared.AvatarKey != nil {
		t.Fatalf("avatar_key not cleared: %v", *cleared.AvatarKey)
	}
}

func TestAvatarEndpoints_NoStoreReturns503(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	u := seedUser(t, repo)

	w := doUpload(t, h, u.ID, pngBytes())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload status: got %d want 503, body=%s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("DELETE", "/me/avatar", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	dw := httptest.NewRecorder()
	h.deleteAvatar(dw, req)
	if dw.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete status: got %d want 503", dw.Code)
	}
}
