package activity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authctx "github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
)

// --- fake store ------------------------------------------------------------

// FakeVideoStore is an in-memory VideoStore for handler tests. Uploaded
// "objects" are recorded by SetObject, which is how a test simulates the
// client's direct-to-S3 PUT — the one step the server never observes.
type FakeVideoStore struct {
	mu       sync.Mutex
	Objects  map[string]int64 // key → size, as if uploaded
	Puts     map[string][]byte
	Orphaned []string
}

var _ VideoStore = (*FakeVideoStore)(nil)

func NewFakeVideoStore() *FakeVideoStore {
	return &FakeVideoStore{Objects: map[string]int64{}, Puts: map[string][]byte{}}
}

// SetObject simulates the client having completed its direct upload.
func (f *FakeVideoStore) SetObject(key string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Objects[key] = size
}

func (f *FakeVideoStore) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://videos.example.test/" + key + "?upload=1", nil
}

func (f *FakeVideoStore) PresignGet(_ context.Context, key string) (string, error) {
	return "https://videos.example.test/" + key + "?get=1", nil
}

func (f *FakeVideoStore) Head(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	size, ok := f.Objects[key]
	if !ok {
		return 0, ErrObjectNotFound
	}
	return size, nil
}

func (f *FakeVideoStore) Put(_ context.Context, key, _ string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := make([]byte, len(body))
	copy(stored, body)
	f.Puts[key] = stored
	return nil
}

func (f *FakeVideoStore) TagOrphaned(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Orphaned = append(f.Orphaned, key)
	return nil
}

// --- harness ---------------------------------------------------------------

func testVideosConfig() VideosConfig {
	return VideosConfig{
		MaxPerActivity:        3,
		MaxUploadBytes:        1000,
		AllowedContentTypes:   []string{"video/mp4", "video/quicktime"},
		PresignWindowHours:    6,
		UploadURLTTLMinutes:   60,
		PosterMaxEdgePx:       640,
		PosterJPEGQuality:     85,
		CaptionMaxChars:       20,
		PendingReapAfterHours: 24,
	}
}

func newVideoHandler(t *testing.T) (*Handler, *FakeVideoStore, *SQLiteVideoRepository, *sql.DB) {
	t.Helper()
	dbc := newMigratedDB(t)
	repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
	videoRepo := NewSQLiteVideoRepository(dbc)
	fake := NewFakeVideoStore()

	h := NewHandler(repo)
	h.SetRegistry(newTestRegistry(t, repo))
	h.SetVideoStore(fake, videoRepo, testVideosConfig())
	// The poster runs through the photo pipeline, so those tunables must be set
	// too — completeVideo reads photoCfg.MaxUploadBytes for the multipart cap.
	h.SetPhotoStore(NewFakePhotoStore(), NewSQLitePhotoRepository(dbc), PhotosConfig{
		MaxUploadBytes: 1 << 20, CaptionMaxChars: 200,
	})
	return h, fake, videoRepo, dbc
}

func authed(r *http.Request, userID string) *http.Request {
	return r.WithContext(authctx.WithUserID(r.Context(), userID))
}

// reserve drives POST /activities/{id}/videos and returns the decoded response.
func reserve(t *testing.T, h *Handler, userID, activityID string, body map[string]any) (*httptest.ResponseRecorder, reserveVideoResponse) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/activities/"+activityID+"/videos", bytes.NewReader(raw))
	req = withParam(authed(req, userID), "id", activityID)
	w := httptest.NewRecorder()
	h.reserveVideo(w, req)

	var env struct {
		Data reserveVideoResponse `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env.Data
}

// complete drives the completion call, optionally attaching a poster JPEG.
func complete(t *testing.T, h *Handler, userID, activityID, videoID string, poster []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	ct := ""
	if poster != nil {
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("poster", "poster.jpg")
		if err != nil {
			t.Fatalf("create poster part: %v", err)
		}
		if _, err := fw.Write(poster); err != nil {
			t.Fatalf("write poster: %v", err)
		}
		mw.Close()
		ct = mw.FormDataContentType()
	}
	req := httptest.NewRequest("POST", "/activities/"+activityID+"/videos/"+videoID+"/complete", &body)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req = withParams(authed(req, userID), "id", activityID, "video_id", videoID)
	w := httptest.NewRecorder()
	h.completeVideo(w, req)
	return w
}

// --- tests -----------------------------------------------------------------

// The happy path end to end, including the step the server never sees: the
// client's direct upload, simulated by SetObject.
func TestVideo_ReserveUploadComplete(t *testing.T) {
	h, store, _, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	w, res := reserve(t, h, userID, activityID, map[string]any{
		"content_type": "video/mp4", "byte_size": 500, "duration_seconds": 12.5,
		"width": 1920, "height": 1080,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("reserve status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if res.VideoID == "" || res.UploadURL == "" {
		t.Fatalf("reserve returned an empty id/url: %+v", res)
	}
	// The upload URL points at the Hive-partitioned key for this activity.
	if !strings.Contains(res.UploadURL, "activity_id="+activityID) {
		t.Errorf("upload url %q does not carry the activity partition", res.UploadURL)
	}

	// Before the object exists, completing is a 409 — not a 500, and not a
	// silent success that would leave a playable-looking row with no bytes.
	if got := complete(t, h, userID, activityID, res.VideoID, nil); got.Code != http.StatusConflict {
		t.Fatalf("complete before upload = %d, want 409; body=%s", got.Code, got.Body.String())
	}

	// Simulate the client's PUT, then complete with a poster.
	key := findReservedKey(t, dbc, res.VideoID)
	store.SetObject(key, 777)

	cw := complete(t, h, userID, activityID, res.VideoID, tinyJPEG(t))
	if cw.Code != http.StatusOK {
		t.Fatalf("complete status = %d, want 200; body=%s", cw.Code, cw.Body.String())
	}

	var env struct {
		Data videoDTO `json:"data"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	// byte_size comes from the store's HEAD (777), NOT the client's claim (500).
	if env.Data.ByteSize != 777 {
		t.Errorf("byte_size = %d, want 777 (the S3-confirmed size, not the client's 500)", env.Data.ByteSize)
	}
	if env.Data.PosterURL == nil {
		t.Errorf("poster_url is nil; want a presigned poster")
	}
	if env.Data.DurationSeconds == nil || *env.Data.DurationSeconds != 12.5 {
		t.Errorf("duration = %v, want 12.5", env.Data.DurationSeconds)
	}
}

// findReservedKey reads the s3_key the handler assigned to a reservation.
func findReservedKey(t *testing.T, dbc *sql.DB, videoID string) string {
	t.Helper()
	var key string
	if err := dbc.QueryRow(`SELECT s3_key FROM activity_video WHERE id = ?`, videoID).Scan(&key); err != nil {
		t.Fatalf("read reserved key: %v", err)
	}
	return key
}

// A video whose poster the browser couldn't produce is still a keepsake: the
// commit succeeds and poster_url is null rather than the upload being rejected.
func TestVideo_CompletesWithoutPoster(t *testing.T) {
	h, store, _, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	_, res := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	store.SetObject(findReservedKey(t, dbc, res.VideoID), 10)

	cw := complete(t, h, userID, activityID, res.VideoID, nil)
	if cw.Code != http.StatusOK {
		t.Fatalf("complete status = %d, want 200; body=%s", cw.Code, cw.Body.String())
	}
	var env struct {
		Data videoDTO `json:"data"`
	}
	_ = json.Unmarshal(cw.Body.Bytes(), &env)
	if env.Data.PosterURL != nil {
		t.Errorf("poster_url = %v, want null", *env.Data.PosterURL)
	}
}

// The size cap's REAL enforcement point. The client under-declares, the object
// lands oversize, and commit rejects it, tags the object, and retires the row.
func TestVideo_OversizeObjectRejectedAtCommit(t *testing.T) {
	h, store, repo, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	// Declares 100 (under the 1000 cap) but uploads 5000.
	_, res := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 100})
	key := findReservedKey(t, dbc, res.VideoID)
	store.SetObject(key, 5000)

	cw := complete(t, h, userID, activityID, res.VideoID, nil)
	if cw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("complete status = %d, want 413; body=%s", cw.Code, cw.Body.String())
	}
	if len(store.Orphaned) == 0 || store.Orphaned[0] != key {
		t.Errorf("oversize object was not tagged for reaping: %v", store.Orphaned)
	}
	got, err := repo.ListByActivity(context.Background(), userID, activityID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("oversize video is visible: %d rows", len(got))
	}
}

func TestVideo_RejectsUnsupportedContainer(t *testing.T) {
	h, _, _, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	w, _ := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/x-matroska", "byte_size": 10})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415; body=%s", w.Code, w.Body.String())
	}
}

func TestVideo_EnforcesPerActivityCap(t *testing.T) {
	h, store, _, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	for i := 0; i < 3; i++ { // MaxPerActivity = 3
		w, res := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve %d = %d; body=%s", i, w.Code, w.Body.String())
		}
		store.SetObject(findReservedKey(t, dbc, res.VideoID), 10)
		_ = complete(t, h, userID, activityID, res.VideoID, nil)
	}
	w, _ := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	if w.Code != http.StatusConflict {
		t.Errorf("4th reserve = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

// Another user's activity is a 404, never a 403 — no existence leak, matching
// the photo surface.
func TestVideo_ForeignActivityIs404(t *testing.T) {
	h, _, _, dbc := newVideoHandler(t)
	activityID := insertTestActivity(t, dbc, "user-1")

	w, _ := reserve(t, h, "user-2", activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// With no bucket configured every endpoint degrades to 503 and nothing panics —
// this is what lets api and infra deploy in either order.
func TestVideo_DegradesTo503WhenUnconfigured(t *testing.T) {
	dbc := newMigratedDB(t)
	repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
	h := NewHandler(repo)
	h.SetRegistry(newTestRegistry(t, repo))
	// SetVideoStore deliberately NOT called.
	activityID := insertTestActivity(t, dbc, "user-1")

	w, _ := reserve(t, h, "user-1", activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("reserve = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "video_storage_unavailable") {
		t.Errorf("missing error code; body=%s", w.Body.String())
	}
}

// THE route-coverage requirement, at the API layer: the detail read carries
// `videos` for EVERY activity type, gated on data and never on activity_type.
// The photo rollout's bug was the client half of this; the server half is
// pinned here so a future type can't silently lose video support.
func TestVideo_DetailReadCarriesVideosForEveryActivityType(t *testing.T) {
	for _, actType := range []ActivityType{
		ActivityRunning, ActivityHiking, ActivityStrengthTraining,
		ActivityWalking, ActivityCycling, ActivityOther,
	} {
		t.Run(string(actType), func(t *testing.T) {
			h, store, _, dbc := newVideoHandler(t)
			userID := "user-1"
			repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
			req := CreateRequest{Type: actType, StartTime: time.Now().UTC()}
			// Endurance types require a positive distance in their details;
			// strength is base-only and takes none.
			if actType != ActivityStrengthTraining {
				req.Details = json.RawMessage(`{"distance_meters":1000}`)
			}
			activityID, err := repo.CreateManual(context.Background(), userID, req)
			if err != nil {
				t.Fatalf("create %s: %v", actType, err)
			}

			w, res := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
			if w.Code != http.StatusCreated {
				t.Fatalf("reserve on %s = %d; body=%s", actType, w.Code, w.Body.String())
			}
			store.SetObject(findReservedKey(t, dbc, res.VideoID), 10)
			if cw := complete(t, h, userID, activityID, res.VideoID, nil); cw.Code != http.StatusOK {
				t.Fatalf("complete on %s = %d; body=%s", actType, cw.Code, cw.Body.String())
			}

			get := httptest.NewRequest("GET", "/activities/"+activityID, nil)
			get = withParam(authed(get, userID), "id", activityID)
			gw := httptest.NewRecorder()
			h.get(gw, get)
			if gw.Code != http.StatusOK {
				t.Fatalf("get %s = %d; body=%s", actType, gw.Code, gw.Body.String())
			}

			var env struct {
				Data struct {
					Videos *[]videoDTO `json:"videos"`
				} `json:"data"`
			}
			if err := json.Unmarshal(gw.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Data.Videos == nil {
				t.Fatalf("videos array absent for %s; body=%s", actType, gw.Body.String())
			}
			if len(*env.Data.Videos) != 1 {
				t.Fatalf("videos len = %d for %s, want 1", len(*env.Data.Videos), actType)
			}
		})
	}
}

// The reaper retires abandoned reservations and tags whatever landed, while
// leaving committed videos alone.
func TestVideo_ReapStalePending(t *testing.T) {
	h, store, repo, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	// One committed video.
	_, ready := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	readyKey := findReservedKey(t, dbc, ready.VideoID)
	store.SetObject(readyKey, 10)
	_ = complete(t, h, userID, activityID, ready.VideoID, nil)

	// One abandoned reservation, backdated past the reap window.
	_, stale := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	staleKey := findReservedKey(t, dbc, stale.VideoID)
	if _, err := dbc.Exec(`UPDATE activity_video SET created_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-48*time.Hour), stale.VideoID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := h.ReapStalePendingVideos(context.Background())
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
	if !contains(store.Orphaned, staleKey) {
		t.Errorf("stale object not tagged: %v", store.Orphaned)
	}
	if contains(store.Orphaned, readyKey) {
		t.Errorf("committed video's object was tagged for reaping: %v", store.Orphaned)
	}
	live, err := repo.ListByActivity(context.Background(), userID, activityID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(live) != 1 || live[0].ID != ready.VideoID {
		t.Errorf("expected only the committed video to survive, got %d rows", len(live))
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// Deleting a video tags both its objects so the bucket lifecycle collects them.
func TestVideo_DeleteTagsBothObjects(t *testing.T) {
	h, store, _, dbc := newVideoHandler(t)
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	_, res := reserve(t, h, userID, activityID, map[string]any{"content_type": "video/mp4", "byte_size": 10})
	key := findReservedKey(t, dbc, res.VideoID)
	store.SetObject(key, 10)
	_ = complete(t, h, userID, activityID, res.VideoID, tinyJPEG(t))

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/activities/%s/videos/%s", activityID, res.VideoID), nil)
	req = withParams(authed(req, userID), "id", activityID, "video_id", res.VideoID)
	w := httptest.NewRecorder()
	h.deleteVideo(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if len(store.Orphaned) != 2 {
		t.Errorf("tagged %d objects, want 2 (video + poster): %v", len(store.Orphaned), store.Orphaned)
	}
}
