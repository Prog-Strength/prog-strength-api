package vectormemory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// memText is the DistilledText newMem stamps ("memory for <session>"). The
// happy-path assertions match against it.
const memText = "memory for s"

// mountRouter mounts both the internal and admin routes (ungated — the admin
// gate is exercised separately in TestHandlerAdminGate403).
func mountRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.MountInternal(r)
	h.MountAdmin(r)
	return r
}

func TestHandlerRetrieveHappyPath(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")
	for _, idx := range []int{0, 1, 2} {
		if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(idx))); err != nil {
			t.Fatalf("insert %d: %v", idx, err)
		}
	}

	emb := &fakeEmbedder{vectors: map[string][]float32{"q0": oneHot(0)}}
	cfg := baseCfg()
	cfg.DistanceThreshold = 0.5
	svc := NewService(repo, emb, &fakeDistiller{}, cfg, testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	body, _ := json.Marshal(map[string]any{"user_id": "userA", "query": "q0"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/memory/retrieve", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data struct {
			Memories []Match `json:"memories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Under the 0.5 default cap only the exact match clears.
	if len(resp.Data.Memories) != 1 {
		t.Fatalf("memories len = %d, want 1; body=%s", len(resp.Data.Memories), rec.Body.String())
	}
	if resp.Data.Memories[0].Text != memText {
		t.Fatalf("matched text = %q, want %q", resp.Data.Memories[0].Text, memText)
	}
}

func TestHandlerRetrieveBadRequest(t *testing.T) {
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	cases := []struct {
		name string
		body []byte
	}{
		{"malformed json", []byte("{not json")},
		{"missing query", []byte(`{"user_id":"userA"}`)},
		{"missing user_id", []byte(`{"query":"hi"}`)},
		{"empty both", []byte(`{}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/memory/retrieve", bytes.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlerRetrieveBestEffortOnError(t *testing.T) {
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	// Failing embedder makes svc.Retrieve return an error; the handler must
	// still answer 200 with an empty list rather than failing the agent's turn.
	emb := &fakeEmbedder{errOn: true}
	svc := NewService(repo, emb, &fakeDistiller{}, baseCfg(), testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	body, _ := json.Marshal(map[string]any{"user_id": "userA", "query": "anything"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/memory/retrieve", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (best-effort); body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Memories []Match `json:"memories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Memories) != 0 {
		t.Fatalf("memories len = %d, want 0 on error", len(resp.Data.Memories))
	}
	// And the JSON must render an empty array, not null.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"memories":[]`)) {
		t.Fatalf("expected empty array memories, got body=%s", rec.Body.String())
	}
}

func TestHandlerDump(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "sA", "userA")
	seedSession(t, db, "sB", "userB")
	if _, err := repo.Insert(ctx, newMem("userA", "sA", oneHot(0))); err != nil {
		t.Fatalf("insert userA: %v", err)
	}
	if _, err := repo.Insert(ctx, newMem("userA", "sA", oneHot(1))); err != nil {
		t.Fatalf("insert userA 2: %v", err)
	}
	if _, err := repo.Insert(ctx, newMem("userB", "sB", oneHot(0))); err != nil {
		t.Fatalf("insert userB: %v", err)
	}

	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	get := func(query string) []memoryDTO {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/memories"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Memories []memoryDTO `json:"memories"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Data.Memories
	}

	// No filter → all three rows; each carries chat provenance.
	all := get("")
	if len(all) != 3 {
		t.Fatalf("unfiltered dump len = %d, want 3", len(all))
	}
	for _, row := range all {
		if row.SourceType != "chat_session" {
			t.Fatalf("dump row source_type = %q, want chat_session", row.SourceType)
		}
		if row.SourceSessionID == nil {
			t.Fatalf("dump row missing source_session_id")
		}
		if row.SourceWorkoutID != nil {
			t.Fatalf("chat dump row unexpectedly has source_workout_id %q", *row.SourceWorkoutID)
		}
	}
	// user_id filter → only userB's row.
	bRows := get("?user_id=userB")
	if len(bRows) != 1 {
		t.Fatalf("userB dump len = %d, want 1", len(bRows))
	}
	if bRows[0].UserID != "userB" {
		t.Fatalf("filtered row user = %q, want userB", bRows[0].UserID)
	}
	// limit=1 caps the page.
	if limited := get("?limit=1"); len(limited) != 1 {
		t.Fatalf("limit=1 dump len = %d, want 1", len(limited))
	}
}

func TestHandlerDumpWorkoutProvenance(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedWorkout(t, db, "w1", "userA")

	wid := "w1"
	if _, err := repo.Insert(ctx, NewMemory{
		UserID: "userA", DistilledText: "left shoulder cranky", SourceType: "workout_note",
		SourceWorkoutID: &wid, EmbeddingModel: activeModel, EmbeddingDim: embedDim,
		Embedding: oneHot(0), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert workout-note memory: %v", err)
	}

	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/memories", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Memories []memoryDTO `json:"memories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Memories) != 1 {
		t.Fatalf("dump len = %d, want 1", len(resp.Data.Memories))
	}
	row := resp.Data.Memories[0]
	if row.SourceType != "workout_note" {
		t.Fatalf("source_type = %q, want workout_note", row.SourceType)
	}
	if row.SourceWorkoutID == nil || *row.SourceWorkoutID != "w1" {
		t.Fatalf("source_workout_id = %v, want w1", row.SourceWorkoutID)
	}
	if row.SourceSessionID != nil {
		t.Fatalf("workout-note row unexpectedly has source_session_id %q", *row.SourceSessionID)
	}
	// And the JSON literally carries the discriminator key.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"source_type":"workout_note"`)) {
		t.Fatalf("expected source_type in JSON, got body=%s", rec.Body.String())
	}
}

func TestHandlerSearch(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")
	for _, idx := range []int{0, 1, 2} {
		if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(idx))); err != nil {
			t.Fatalf("insert %d: %v", idx, err)
		}
	}

	emb := &fakeEmbedder{vectors: map[string][]float32{"q0": oneHot(0)}}
	cfg := baseCfg()
	cfg.DistanceThreshold = 0.5
	svc := NewService(repo, emb, &fakeDistiller{}, cfg, testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	type searchResp struct {
		Data struct {
			Threshold float64 `json:"threshold"`
			Matches   []Match `json:"matches"`
		} `json:"data"`
	}
	post := func(body map[string]any) searchResp {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/memories/search", bytes.NewReader(b)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp searchResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	// Default (omitted threshold) → config default 0.5 echoed, only the exact
	// match clears; the match carries a distance.
	def := post(map[string]any{"user_id": "userA", "query": "q0"})
	if def.Data.Threshold != 0.5 {
		t.Fatalf("default threshold echoed = %v, want 0.5", def.Data.Threshold)
	}
	if len(def.Data.Matches) != 1 {
		t.Fatalf("default matches = %d, want 1", len(def.Data.Matches))
	}
	if def.Data.Matches[0].Distance != 0 {
		t.Fatalf("match distance = %v, want 0", def.Data.Matches[0].Distance)
	}

	// Explicit threshold 0 → full sweep, all three, echoed threshold 0.
	full := post(map[string]any{"user_id": "userA", "query": "q0", "threshold": 0})
	if full.Data.Threshold != 0 {
		t.Fatalf("override threshold echoed = %v, want 0", full.Data.Threshold)
	}
	if len(full.Data.Matches) != 3 {
		t.Fatalf("full-sweep matches = %d, want 3", len(full.Data.Matches))
	}

	// k cap → 1 result even on a full sweep.
	capped := post(map[string]any{"user_id": "userA", "query": "q0", "threshold": 0, "k": 1})
	if len(capped.Data.Matches) != 1 {
		t.Fatalf("k=1 matches = %d, want 1", len(capped.Data.Matches))
	}
}

func TestHandlerSearchBadRequest(t *testing.T) {
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	srv := mountRouter(NewHandler(svc, testLogger()))

	for _, body := range [][]byte{
		[]byte("{bad"),
		[]byte(`{"user_id":"userA"}`),
		[]byte(`{"query":"hi"}`),
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/memories/search", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q → status %d, want 400", body, rec.Code)
		}
	}
}

// TestHandlerAdminGate403 mounts the admin routes behind the REAL
// auth.RequireAdmin gate with a non-admin user injected and asserts 403 on both
// admin verbs. Mirrors beta's TestHandler_NonAdmin403EveryVerb.
func TestHandlerAdminGate403(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))

	// Seed a NON-admin user.
	u := &user.User{
		Email:        "notadmin@example.com",
		DisplayName:  "Not Admin",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
	}
	if err := userRepo.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	h := NewHandler(svc, testLogger())

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), u.ID)))
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin(userRepo, []string{"admin@example.com"}))
		h.MountAdmin(r)
	})

	cases := []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/admin/memories", nil},
		{http.MethodPost, "/admin/memories/search", []byte(`{"user_id":"userA","query":"hi"}`)},
	}
	for _, tc := range cases {
		var rdr *bytes.Reader
		if tc.body != nil {
			rdr = bytes.NewReader(tc.body)
		} else {
			rdr = bytes.NewReader(nil)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, rdr))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}
