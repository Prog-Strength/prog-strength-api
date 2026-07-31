package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// doGetDetail drives the detail GET (h.get) for activityID as userID and
// returns the recorder.
func doGetDetail(t *testing.T, h *Handler, userID, activityID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/activities/"+activityID, nil)
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID)
	w := httptest.NewRecorder()
	h.get(w, req)
	return w
}

// doDeleteActivity drives the activity delete handler (h.delete) for activityID
// as userID and returns the recorder.
func doDeleteActivity(t *testing.T, h *Handler, userID, activityID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/activities/"+activityID, nil)
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), userID)), "id", activityID)
	w := httptest.NewRecorder()
	h.delete(w, req)
	return w
}

// A detail GET on an activity with photos includes a photos array ordered
// (position, id), each with presigned full + thumb URLs, filtered to live.
func TestGetDetail_IncludesPhotosOrdered(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	p0 := mustInsertPhoto(t, photoRepo, testUserID, a) // position 0
	p1 := mustInsertPhoto(t, photoRepo, testUserID, a) // position 1
	p2 := mustInsertPhoto(t, photoRepo, testUserID, a) // position 2

	w := doGetDetail(t, h, testUserID, a)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if env.Data.Photos == nil {
		t.Fatal("photos is nil; want a present array")
	}
	got := *env.Data.Photos
	if len(got) != 3 {
		t.Fatalf("photos len = %d, want 3", len(got))
	}
	wantOrder := []string{p0.ID, p1.ID, p2.ID}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("photos[%d].id = %q, want %q (position order)", i, got[i].ID, id)
		}
		if got[i].Position != i {
			t.Errorf("photos[%d].position = %d, want %d", i, got[i].Position, i)
		}
		if got[i].URL == "" || got[i].ThumbURL == "" {
			t.Errorf("photos[%d] missing presigned url(s): url=%q thumb=%q", i, got[i].URL, got[i].ThumbURL)
		}
	}
}

// A soft-deleted photo is filtered out of the detail read's photos array.
func TestGetDetail_ExcludesSoftDeletedPhoto(t *testing.T) {
	h, _, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	live := mustInsertPhoto(t, photoRepo, testUserID, a)
	gone := mustInsertPhoto(t, photoRepo, testUserID, a)
	if err := photoRepo.SoftDelete(context.Background(), testUserID, a, gone.ID); err != nil {
		t.Fatalf("SoftDelete photo: %v", err)
	}

	w := doGetDetail(t, h, testUserID, a)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Photos == nil || len(*env.Data.Photos) != 1 {
		t.Fatalf("photos = %v, want exactly 1 (soft-deleted excluded)", env.Data.Photos)
	}
	if (*env.Data.Photos)[0].ID != live.ID {
		t.Errorf("photos[0].id = %q, want the live photo %q", (*env.Data.Photos)[0].ID, live.ID)
	}
}

// With photo storage configured but no photos on the activity, the detail read
// carries an empty (present, non-null) photos array.
func TestGetDetail_EmptyPhotosArrayWhenConfigured(t *testing.T) {
	h, _, _, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)

	w := doGetDetail(t, h, testUserID, a)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// The raw body must carry "photos":[] — present, not null, not absent.
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	got, ok := raw.Data["photos"]
	if !ok {
		t.Fatalf("photos key absent; want present as [] when storage is configured; body=%s", w.Body.String())
	}
	if string(got) != "[]" {
		t.Errorf("photos = %s, want []", string(got))
	}
}

// When photo storage is unconfigured (SetPhotoStore never called), the detail
// read omits the photos key entirely.
func TestGetDetail_OmitsPhotosWhenUnconfigured(t *testing.T) {
	h, _, repo := newTestHandler(t)
	a := newActivity(testUserID, IngestManualTCX, "x", time.Now().UTC(), 1000, 300)
	if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doGetDetail(t, h, testUserID, a.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw.Data["photos"]; ok {
		t.Errorf("photos key present; want omitted when storage is unconfigured; body=%s", w.Body.String())
	}
}

// Deleting an activity is a SOFT delete: its photo rows survive untouched, the
// photos are hidden from the detail read (parent-live filter), and NO photo
// object is tagged orphaned (a soft delete is reversible).
func TestDeleteActivity_SoftDelete_LeavesPhotosUntaggedAndHidden(t *testing.T) {
	h, fake, photoRepo, dbc := newPhotoHandler(t, testPhotosConfig())
	a := insertTestActivity(t, dbc, testUserID)
	mustInsertPhoto(t, photoRepo, testUserID, a)
	mustInsertPhoto(t, photoRepo, testUserID, a)

	wd := doDeleteActivity(t, h, testUserID, a)
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", wd.Code, wd.Body.String())
	}

	// No orphan tagging on a soft delete.
	if len(fake.Orphaned) != 0 {
		t.Errorf("orphaned = %v, want none on soft delete", fake.Orphaned)
	}

	// The photo rows are intact (the soft delete never touched them).
	var rows int
	if err := dbc.QueryRow(`SELECT COUNT(*) FROM activity_photo WHERE activity_id = ?`, a).Scan(&rows); err != nil {
		t.Fatalf("count photo rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("photo rows = %d, want 2 (soft delete leaves them)", rows)
	}

	// The detail read is now 404 (parent hidden) — the photos are unreachable
	// but recoverable by restoring the activity.
	wg := doGetDetail(t, h, testUserID, a)
	if wg.Code != http.StatusNotFound {
		t.Errorf("get after soft delete = %d, want 404", wg.Code)
	}
}
