package activity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// testPhotosConfig returns a PhotosConfig with sane defaults for the write-path
// tests. Individual tests override fields (e.g. MaxUploadBytes) as needed.
func testPhotosConfig() PhotosConfig {
	return PhotosConfig{
		MaxPerActivity:     3,
		MaxUploadBytes:     5 << 20,
		FullMaxEdgePx:      1600,
		FullJPEGQuality:    82,
		ThumbMaxEdgePx:     400,
		ThumbJPEGQuality:   75,
		PresignWindowHours: 1,
		CaptionMaxChars:    280,
	}
}

// newPhotoHandler builds a Handler wired with a real photo repository + SQLite
// activity repository over one shared migrated DB, a registry (so the parent
// activity's type validates in buildPhotoKey), and the supplied fake store/cfg.
// It returns the handler, the fake store, the photo repo, and the shared DB.
func newPhotoHandler(t *testing.T, cfg PhotosConfig) (*Handler, *FakePhotoStore, *SQLitePhotoRepository, *sql.DB) {
	t.Helper()
	dbc := newMigratedDB(t)
	repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
	photoRepo := NewSQLitePhotoRepository(dbc)
	fake := NewFakePhotoStore()

	h := NewHandler(repo)
	h.SetRegistry(newTestRegistry(t, repo))
	h.SetPhotoStore(fake, photoRepo, cfg)
	return h, fake, photoRepo, dbc
}

// tinyJPEG encodes a 4x4 solid-color image as JPEG — a minimal-but-valid image
// for the happy path.
func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 120, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode tiny jpeg: %v", err)
	}
	return buf.Bytes()
}

// photoMultipart builds a multipart/form-data body carrying photoBytes under the
// "photo" field and, when caption != nil, a caption form field.
func photoMultipart(t *testing.T, photoBytes []byte, caption *string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("photo", "photo.jpg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(photoBytes); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if caption != nil {
		if err := mw.WriteField("caption", *caption); err != nil {
			t.Fatalf("write caption: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// doUpload drives uploadPhoto with a multipart photo upload as userID.
func doUpload(t *testing.T, h *Handler, userID, activityID string, photoBytes []byte, caption *string) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := photoMultipart(t, photoBytes, caption)
	req := httptest.NewRequest("POST", "/activities/"+activityID+"/photos", body)
	req.Header.Set("Content-Type", ct)
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID)
	w := httptest.NewRecorder()
	h.uploadPhoto(w, req)
	return w
}

// withParams attaches multiple chi URL params to a single route context on the
// request (withParam only carries one, and calling it twice replaces the first).
func withParams(req *http.Request, kv ...string) *http.Request {
	rc := chi.NewRouteContext()
	for i := 0; i+1 < len(kv); i += 2 {
		rc.URLParams.Add(kv[i], kv[i+1])
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

type photoEnvelope struct {
	Message string   `json:"message"`
	Data    photoDTO `json:"data"`
}

type photoListEnvelope struct {
	Message string     `json:"message"`
	Data    []photoDTO `json:"data"`
}

func liveCount(t *testing.T, r *SQLitePhotoRepository, activityID string) int {
	t.Helper()
	n, err := r.CountLive(context.Background(), activityID)
	if err != nil {
		t.Fatalf("CountLive: %v", err)
	}
	return n
}

// --- POST upload ---------------------------------------------------------

func TestUploadPhoto_HappyPath(t *testing.T) {
	h, fake, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	caption := "leg day"
	w := doUpload(t, h, testUserID, a, tinyJPEG(t), &caption)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	var env photoEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ID == "" {
		t.Error("expected photo id")
	}
	if env.Data.Position != 0 {
		t.Errorf("position = %d, want 0", env.Data.Position)
	}
	if env.Data.Caption == nil || *env.Data.Caption != "leg day" {
		t.Errorf("caption = %v, want 'leg day'", env.Data.Caption)
	}
	if env.Data.URL == "" || env.Data.ThumbURL == "" {
		t.Errorf("expected both presigned urls, got url=%q thumb=%q", env.Data.URL, env.Data.ThumbURL)
	}
	if env.Data.Width <= 0 || env.Data.Height <= 0 {
		t.Errorf("expected positive dimensions, got %dx%d", env.Data.Width, env.Data.Height)
	}

	if n := liveCount(t, photoRepo, a); n != 1 {
		t.Errorf("live count = %d, want 1", n)
	}
	// Two objects were Put (full + thumb) and none orphaned.
	if fake.PutCount != 2 {
		t.Errorf("PutCount = %d, want 2", fake.PutCount)
	}
	if len(fake.Orphaned) != 0 {
		t.Errorf("orphaned = %v, want none", fake.Orphaned)
	}
}

func TestUploadPhoto_Oversize413(t *testing.T) {
	cfg := testPhotosConfig()
	cfg.MaxUploadBytes = 10 // absurdly tiny so even the tiny JPEG blows the cap
	h, _, photoRepo, dbc := newPhotoHandler(t, cfg)
	a := insertTestActivity(t, dbc, testUserID)

	w := doUpload(t, h, testUserID, a, tinyJPEG(t), nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large", env.Code)
	}
	if n := liveCount(t, photoRepo, a); n != 0 {
		t.Errorf("live count = %d, want 0", n)
	}
}

func TestUploadPhoto_DisallowedType415(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	w := doUpload(t, h, testUserID, a, []byte("this is definitely not an image, just plain text"), nil)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code != "unsupported_media_type" {
		t.Errorf("code = %q, want unsupported_media_type", env.Code)
	}
	if n := liveCount(t, photoRepo, a); n != 0 {
		t.Errorf("live count = %d, want 0", n)
	}
}

func TestUploadPhoto_OverLimit409(t *testing.T) {
	cfg := testPhotosConfig()
	cfg.MaxPerActivity = 2
	h, _, photoRepo, dbc := newPhotoHandler(t, cfg)
	a := insertTestActivity(t, dbc, testUserID)

	// Pre-insert the limit directly through the repo.
	mustInsertPhoto(t, photoRepo, testUserID, a)
	mustInsertPhoto(t, photoRepo, testUserID, a)

	w := doUpload(t, h, testUserID, a, tinyJPEG(t), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code != "photo_limit_reached" {
		t.Errorf("code = %q, want photo_limit_reached", env.Code)
	}
	if n := liveCount(t, photoRepo, a); n != 2 {
		t.Errorf("live count = %d, want 2", n)
	}
}

func TestUploadPhoto_OtherUsersActivity404(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	// Activity owned by userB.
	a := insertTestActivity(t, dbc, "userB")

	// Request as userA — must not leak existence.
	w := doUpload(t, h, "userA", a, tinyJPEG(t), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code != "not_found" {
		t.Errorf("code = %q, want not_found", env.Code)
	}
	if n := liveCount(t, photoRepo, a); n != 0 {
		t.Errorf("live count = %d, want 0", n)
	}
}

func TestUploadPhoto_SecondPutFailsOrphansFirst(t *testing.T) {
	h, fake, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	fake.PutFunc = func(callN int) error {
		if callN == 2 {
			return errors.New("boom")
		}
		return nil
	}

	w := doUpload(t, h, testUserID, a, tinyJPEG(t), nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	// NO row written.
	if n := liveCount(t, photoRepo, a); n != 0 {
		t.Errorf("live count = %d, want 0", n)
	}
	// The first (full) key was best-effort orphaned.
	if len(fake.Orphaned) != 1 {
		t.Fatalf("orphaned = %v, want exactly the full key", fake.Orphaned)
	}
	// The first successful Put's key is the one that must be orphaned.
	var fullKey string
	for k := range fake.Puts {
		fullKey = k
	}
	if fake.Orphaned[0] != fullKey {
		t.Errorf("orphaned = %q, want full key %q", fake.Orphaned[0], fullKey)
	}
}

func TestUploadPhoto_StorageUnconfigured503(t *testing.T) {
	dbc := newMigratedDB(t)
	repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
	h := NewHandler(repo)
	h.SetRegistry(newTestRegistry(t, repo))
	// No SetPhotoStore — store/repo are nil.
	a := insertTestActivity(t, dbc, testUserID)

	w := doUpload(t, h, testUserID, a, tinyJPEG(t), nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code != "photo_storage_unavailable" {
		t.Errorf("code = %q, want photo_storage_unavailable", env.Code)
	}
}

// --- PATCH caption -------------------------------------------------------

func doPatchCaption(t *testing.T, h *Handler, userID, activityID, photoID, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/activities/"+activityID+"/photos/"+photoID, bytes.NewBufferString(rawBody))
	req = withParams(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID, "photo_id", photoID)
	w := httptest.NewRecorder()
	h.patchPhotoCaption(w, req)
	return w
}

func TestPatchPhotoCaption_Valid(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)
	p := mustInsertPhoto(t, photoRepo, testUserID, a)

	w := doPatchCaption(t, h, testUserID, a, p.ID, `{"caption":"new caption"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env photoEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Caption == nil || *env.Data.Caption != "new caption" {
		t.Errorf("caption = %v, want 'new caption'", env.Data.Caption)
	}

	// Persisted.
	got, err := photoRepo.Get(context.Background(), testUserID, a, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Caption == nil || *got.Caption != "new caption" {
		t.Errorf("persisted caption = %v", got.Caption)
	}
}

func TestPatchPhotoCaption_OverLength400(t *testing.T) {
	cfg := testPhotosConfig()
	cfg.CaptionMaxChars = 5
	h, _, photoRepo, dbc := newPhotoHandler(t, cfg)
	a := insertTestActivity(t, dbc, testUserID)
	p := mustInsertPhoto(t, photoRepo, testUserID, a)

	w := doPatchCaption(t, h, testUserID, a, p.ID, `{"caption":"way too long"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// --- PUT order -----------------------------------------------------------

func doReorder(t *testing.T, h *Handler, userID, activityID string, photoIDs []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"photo_ids": photoIDs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("PUT", "/activities/"+activityID+"/photos/order", bytes.NewReader(body))
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID)
	w := httptest.NewRecorder()
	h.reorderPhotos(w, req)
	return w
}

func TestReorderPhotos_ValidFullReorder(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)
	p0 := mustInsertPhoto(t, photoRepo, testUserID, a)
	p1 := mustInsertPhoto(t, photoRepo, testUserID, a)
	p2 := mustInsertPhoto(t, photoRepo, testUserID, a)

	// Reverse the order.
	order := []string{p2.ID, p1.ID, p0.ID}
	w := doReorder(t, h, testUserID, a, order)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env photoListEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 3 {
		t.Fatalf("len = %d, want 3", len(env.Data))
	}
	for i, want := range order {
		if env.Data[i].ID != want {
			t.Errorf("position %d id = %q, want %q", i, env.Data[i].ID, want)
		}
		if env.Data[i].Position != i {
			t.Errorf("position field %d = %d, want %d", i, env.Data[i].Position, i)
		}
	}

	// Idempotent: re-submit the same order.
	w2 := doReorder(t, h, testUserID, a, order)
	if w2.Code != http.StatusOK {
		t.Fatalf("idempotent reorder status = %d, want 200", w2.Code)
	}
}

func TestReorderPhotos_Rejections(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)
	p0 := mustInsertPhoto(t, photoRepo, testUserID, a)
	p1 := mustInsertPhoto(t, photoRepo, testUserID, a)
	p2 := mustInsertPhoto(t, photoRepo, testUserID, a)

	cases := map[string][]string{
		"subset":    {p0.ID, p1.ID},                 // missing p2
		"extra":     {p0.ID, p1.ID, p2.ID, "extra"}, // unknown id
		"duplicate": {p0.ID, p1.ID, p1.ID},          // dup, right length
	}
	for name, order := range cases {
		t.Run(name, func(t *testing.T) {
			w := doReorder(t, h, testUserID, a, order)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// --- DELETE --------------------------------------------------------------

func doDelete(t *testing.T, h *Handler, userID, activityID, photoID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/activities/"+activityID+"/photos/"+photoID, nil)
	req = withParams(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID, "photo_id", photoID)
	w := httptest.NewRecorder()
	h.deletePhoto(w, req)
	return w
}

func TestDeletePhoto_SoftDeletesAndOrphansBothKeys(t *testing.T) {
	h, fake, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)
	p := mustInsertPhoto(t, photoRepo, testUserID, a)

	w := doDelete(t, h, testUserID, a, p.ID)
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 200/204; body=%s", w.Code, w.Body.String())
	}

	// Gone from ListByActivity.
	got, err := photoRepo.ListByActivity(context.Background(), testUserID, a)
	if err != nil {
		t.Fatalf("ListByActivity: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("live photos = %d, want 0", len(got))
	}

	// Both keys tagged orphaned.
	if len(fake.Orphaned) != 2 {
		t.Fatalf("orphaned = %v, want 2 (full + thumb)", fake.Orphaned)
	}
	seen := map[string]bool{}
	for _, k := range fake.Orphaned {
		seen[k] = true
	}
	if !seen[p.S3Key] || !seen[p.ThumbS3Key] {
		t.Errorf("orphaned = %v, want both %q and %q", fake.Orphaned, p.S3Key, p.ThumbS3Key)
	}
}

func TestDeletePhoto_NotFound404(t *testing.T) {
	h, _, _, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	w := doDelete(t, h, testUserID, a, "nonexistent")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}
