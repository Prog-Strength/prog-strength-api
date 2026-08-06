package activity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
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
