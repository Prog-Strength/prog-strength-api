// The registry contract test: the executable guarantee that a brand-new
// base-only activity type flows through every surface with NO code outside
// its Descriptor. "shadowboxing" below is registered in this test only —
// nothing in internal/activity, internal/dashboard, or internal/server knows
// it exists. If a future change makes any assertion here require touching a
// switch statement (or any code) outside the descriptor + registration, the
// "new types come free" invariant is broken and this test is the alarm.
//
// External test package deliberately: it wires the real dashboard handler
// (which imports this package) next to the activity handler over one
// database, which an in-package test file cannot do without an import cycle.
package activity_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity/strength"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/dashboard"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/snapshot"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// shadowboxingType is the fake base-only type. Not an ActivityType constant
// anywhere in production code — that's the point.
const shadowboxingType = activity.ActivityType("shadowboxing")

// newShadowboxingDescriptor is the ENTIRE per-type surface a base-only type
// implements: no DetailStore, no migration, no handler or switch edits.
// ValidateCreate requires a positive duration; Summarize renders a
// name-or-default title with a duration chip — the same card the timeline
// hydrator and the unified list share via RenderSummaries.
func newShadowboxingDescriptor() *activity.Descriptor {
	return &activity.Descriptor{
		Type: shadowboxingType,
		ValidateCreate: func(req activity.CreateRequest) error {
			if req.DurationSeconds == nil || *req.DurationSeconds <= 0 {
				return fmt.Errorf("shadowboxing needs a positive duration_seconds")
			}
			return nil
		},
		Summarize: func(a activity.Activity, _ any) activity.Summary {
			title := "Shadowboxing"
			if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
				title = *a.Name
			}
			dur := activity.FormatDuration(float64(a.DurationSeconds))
			return activity.Summary{Title: title, Subtitle: dur, Metrics: []string{dur}}
		},
	}
}

// newContractRegistry mirrors server.go's registry construction (the four
// endurance descriptors, same detail stores) plus the test-only shadowboxing
// descriptor. The production registry in server.go keeps exactly the real
// descriptors; shadowboxing exists only inside this test binary.
func newContractRegistry(db *sql.DB) *activity.Registry {
	return activity.NewRegistry(
		activity.NewEnduranceDescriptor(activity.ActivityRunning, activity.NewSQLiteEnduranceDetailStore(db, activity.ActivityRunning)),
		activity.NewEnduranceDescriptor(activity.ActivityWalking, activity.NewSQLiteEnduranceDetailStore(db, activity.ActivityWalking)),
		activity.NewEnduranceDescriptor(activity.ActivityCycling, activity.NewSQLiteEnduranceDetailStore(db, activity.ActivityCycling)),
		activity.NewEnduranceDescriptor(activity.ActivityOther, activity.NewSQLiteEnduranceDetailStore(db, activity.ActivityOther)),
		newShadowboxingDescriptor(),
	)
}

// contractEnv is the wired system under test: the activity handler (unified
// /activities surface, shadowboxing-extended registry) and the real dashboard
// handler mounted on one router over one SQLite database, behind a fake auth
// middleware — the same shape server.go assembles.
type contractEnv struct {
	srv      http.Handler
	db       *sql.DB
	registry *activity.Registry
	repos    contractRepos
	userID   string
	// published records every timeline post the wired handler emits, so the
	// contract can assert feed publishing the same way it asserts the HTTP
	// surface: through the real handler, with nothing type-aware in between.
	published *contractPublisher
}

// contractPublisher is a timeline.Publisher that records instead of writing.
// The real one is server-wired; the contract only needs to see what the
// handler decided to publish.
type contractPublisher struct {
	refs []timeline.PostRef
}

func (p *contractPublisher) EnsurePost(_ context.Context, ref timeline.PostRef) error {
	p.refs = append(p.refs, ref)
	return nil
}

type contractRepos struct {
	activity   *activity.SQLiteRepository
	workout    *strength.SQLiteRepository
	exercise   *exercise.SQLiteRepository
	steps      *steps.SQLiteRepository
	nutrition  *nutrition.SQLiteRepository
	bodyweight *bodyweight.SQLiteRepository
	user       *user.SQLiteRepository
}

func newContractEnv(t *testing.T) *contractEnv {
	t.Helper()
	db := dbtest.New(t)
	rp := contractRepos{
		activity:   activity.NewSQLiteRepository(db, activity.NewMemoryArchiver()),
		workout:    strength.NewSQLiteRepository(db),
		exercise:   exercise.NewSQLiteRepository(db),
		steps:      steps.NewSQLiteRepository(db),
		nutrition:  nutrition.NewSQLiteRepository(db),
		bodyweight: bodyweight.NewSQLiteRepository(db),
		user:       user.NewSQLiteRepository(db),
	}

	u := &user.User{
		Email:        "contract@example.com",
		DisplayName:  "Contract User",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
		Timezone:     "UTC",
	}
	if err := rp.user.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	reg := newContractRegistry(db)
	ah := activity.NewHandler(rp.activity)
	ah.SetRegistry(reg)
	pub := &contractPublisher{}
	ah.SetPublisher(pub)

	dh := dashboard.NewHandler(rp.activity, rp.workout, rp.exercise, rp.steps,
		rp.nutrition, rp.bodyweight, rp.user,
		whoopconn.NewSQLiteRepository(db), whooprecovery.NewSQLiteRepository(db),
		dashboard.NewSQLiteLayoutRepository(db))

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), u.ID)))
		})
	})
	ah.Mount(r)
	dh.Mount(r)

	return &contractEnv{srv: r, db: db, registry: reg, repos: rp, userID: u.ID, published: pub}
}

func (e *contractEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	return w
}

// contractItem is the slice of the wire-level activity DTO the contract
// asserts on. Decoded independently of the package's internal DTO structs so
// the test pins the JSON contract, not the Go one.
type contractItem struct {
	ID              string            `json:"id"`
	ActivityType    string            `json:"activity_type"`
	Name            *string           `json:"name"`
	DurationSeconds int               `json:"duration_seconds"`
	Summary         *activity.Summary `json:"summary"`
}

func decodeContractItem(t *testing.T, w *httptest.ResponseRecorder) contractItem {
	t.Helper()
	var env struct {
		Data contractItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode item envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Data
}

func decodeContractList(t *testing.T, w *httptest.ResponseRecorder) []contractItem {
	t.Helper()
	var env struct {
		Data struct {
			Activities []contractItem `json:"activities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Data.Activities
}

// TestContract_BaseOnlyType_UnifiedSurface: POST/GET/list/PUT/DELETE for a
// type the production code has never heard of, through the exact routes
// server.go mounts.
func TestContract_BaseOnlyType_UnifiedSurface(t *testing.T) {
	env := newContractEnv(t)

	// Descriptor validation is enforced through the generic path: a
	// duration-less shadowboxing create is the descriptor's 400, not a 422 —
	// the registry recognized the type; only its own rule failed.
	w := env.do(t, "POST", "/activities",
		`{"activity_type":"shadowboxing","start_time":"2026-07-20T18:00:00Z"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duration-less create status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "duration_seconds") {
		t.Errorf("400 body should carry the descriptor's message; body=%s", w.Body.String())
	}

	// The unknown-type 422 still fires for genuinely unregistered types —
	// proof the registry is the ONLY gate shadowboxing had to pass, and that
	// no other switch statement anywhere in the flow had to learn the type.
	w = env.do(t, "POST", "/activities",
		`{"activity_type":"kickboxing","start_time":"2026-07-20T18:00:00Z","duration_seconds":1800}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unregistered type status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "shadowboxing") {
		t.Errorf("422 body should list shadowboxing among the valid types; body=%s", w.Body.String())
	}

	// Create: base row only, via Repository.CreateManual (nil Details, nil
	// Create on the descriptor).
	w = env.do(t, "POST", "/activities",
		`{"activity_type":"shadowboxing","start_time":"2026-07-20T18:00:00Z","duration_seconds":1800,"name":"Evening rounds"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	created := decodeContractItem(t, w)
	if created.ID == "" || created.ActivityType != "shadowboxing" {
		t.Fatalf("created = %+v, want a shadowboxing row with an id", created)
	}

	// All-types list: present with the descriptor-rendered card.
	w = env.do(t, "GET", "/activities", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", w.Code, w.Body.String())
	}
	all := decodeContractList(t, w)
	if len(all) != 1 {
		t.Fatalf("list = %d items, want 1", len(all))
	}
	item := all[0]
	if item.Summary == nil {
		t.Fatal("list item has no summary")
	}
	if item.Summary.Title != "Evening rounds" {
		t.Errorf("summary title = %q, want %q", item.Summary.Title, "Evening rounds")
	}
	if item.Summary.Subtitle != "30:00" {
		t.Errorf("summary subtitle = %q, want %q", item.Summary.Subtitle, "30:00")
	}
	if len(item.Summary.Metrics) != 1 || item.Summary.Metrics[0] != "30:00" {
		t.Errorf("summary metrics = %v, want [30:00]", item.Summary.Metrics)
	}

	// Type-filtered list: ?type= validates through the registry, so the
	// test-registered type is filterable with zero handler changes.
	w = env.do(t, "GET", "/activities?type=shadowboxing", "")
	if w.Code != http.StatusOK {
		t.Fatalf("filtered list status = %d; body=%s", w.Code, w.Body.String())
	}
	filtered := decodeContractList(t, w)
	if len(filtered) != 1 || filtered[0].ID != created.ID {
		t.Fatalf("?type=shadowboxing = %+v, want exactly the created session", filtered)
	}

	// Detail read: base fields only; a base-only type's response must omit
	// the details key entirely (no detail table, no DetailStore).
	w = env.do(t, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", w.Code, w.Body.String())
	}
	got := decodeContractItem(t, w)
	if got.Name == nil || *got.Name != "Evening rounds" {
		t.Errorf("name = %v, want Evening rounds", got.Name)
	}
	if got.DurationSeconds != 1800 {
		t.Errorf("duration = %d, want 1800", got.DurationSeconds)
	}
	var raw struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw detail: %v", err)
	}
	if _, present := raw.Data["details"]; present {
		t.Errorf("detail response carries a details key for a base-only type: %v", raw.Data["details"])
	}
	if raw.Data["activity_type"] != "shadowboxing" {
		t.Errorf("activity_type = %v, want shadowboxing", raw.Data["activity_type"])
	}

	// PUT rename round-trip through the generic UpdateBase path (nil Update
	// on the descriptor). ValidateCreate applies to updates too.
	w = env.do(t, "PUT", "/activities/"+created.ID,
		`{"activity_type":"shadowboxing","start_time":"2026-07-20T18:00:00Z","duration_seconds":2100,"name":"Evening rounds v2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d; body=%s", w.Code, w.Body.String())
	}
	w = env.do(t, "GET", "/activities/"+created.ID, "")
	got = decodeContractItem(t, w)
	if got.Name == nil || *got.Name != "Evening rounds v2" {
		t.Errorf("post-PUT name = %v, want Evening rounds v2", got.Name)
	}
	if got.DurationSeconds != 2100 {
		t.Errorf("post-PUT duration = %d, want 2100", got.DurationSeconds)
	}

	// Soft delete round-trip: 204, then the row reads as gone everywhere.
	w = env.do(t, "DELETE", "/activities/"+created.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body=%s", w.Code, w.Body.String())
	}
	w = env.do(t, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", w.Code)
	}
	w = env.do(t, "GET", "/activities", "")
	if remaining := decodeContractList(t, w); len(remaining) != 0 {
		t.Fatalf("list after delete = %d items, want 0", len(remaining))
	}
}

// TestContract_BaseOnlyType_RendersTimelineCard asserts the timeline card
// path at its real seam: RenderSummary/RenderSummaries are exactly what the
// timeline hydrator calls to render post cards (and what the unified list
// calls — one card, shared). A registered Summarize is therefore all a new
// type needs for its card to render.
//
// Card rendering and post publishing used to be different contracts — the
// latter needed a per-type source_type plus a CHECK-widening migration, so a
// registered type rendered a card it would never get a post for. Migration 046
// collapsed the feed's per-sport source types into one `activity` value; both
// now come free, and TestContract_BaseOnlyType_PostsToTimeline below is the
// guard for the publishing half.
func TestContract_BaseOnlyType_RendersTimelineCard(t *testing.T) {
	env := newContractEnv(t)

	name := "Sparring prep"
	a := activity.Activity{
		ID:              "sb1",
		UserID:          env.userID,
		ActivityType:    shadowboxingType,
		Name:            &name,
		DurationSeconds: 1800,
		StartTime:       time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC),
	}

	s, ok := activity.RenderSummary(env.registry, a, nil)
	if !ok {
		t.Fatal("RenderSummary ok=false: the hydrator would drop this type's card")
	}
	if s.Title != "Sparring prep" || s.Subtitle != "30:00" {
		t.Errorf("card = %+v, want title 'Sparring prep' subtitle '30:00'", s)
	}

	// Batch path (the timeline hydrator's entry point; the unified list
	// composes LoadDetailsBulk + RenderSummary itself): a nil DetailStore
	// means no bulk load; the card renders from the base row.
	sums := activity.RenderSummaries(context.Background(), env.registry, env.userID, []activity.Activity{a})
	got, ok := sums["sb1"]
	if !ok {
		t.Fatal("RenderSummaries omitted the shadowboxing card")
	}
	if got.Title != s.Title || got.Subtitle != s.Subtitle {
		t.Errorf("batch card %+v != single card %+v", got, s)
	}

	// A default-titled card when the session is unnamed.
	a.Name = nil
	s, ok = activity.RenderSummary(env.registry, a, nil)
	if !ok || s.Title != "Shadowboxing" {
		t.Errorf("unnamed card = %+v (ok=%v), want default title Shadowboxing", s, ok)
	}
}

// TestContract_BaseOnlyType_PostsToTimeline: logging a session of a type
// production has never heard of publishes a feed post, through the real POST
// /activities handler with no type-aware code in between. This is the half of
// the timeline contract that used to be missing — the gap that made hiking
// invisible in the social feed (issue #90) — and it holds now because the feed
// keys posts on the coarse `activity` source domain and reads the sport from
// activities.activity_type.
func TestContract_BaseOnlyType_PostsToTimeline(t *testing.T) {
	env := newContractEnv(t)

	w := env.do(t, http.MethodPost, "/activities", fmt.Sprintf(
		`{"activity_type":%q,"start_time":"2026-07-20T18:00:00Z","name":"Sparring prep","duration_seconds":1800}`,
		shadowboxingType))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /activities = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	created := decodeContractItem(t, w)

	if len(env.published.refs) != 1 {
		t.Fatalf("published %d posts, want 1: %+v", len(env.published.refs), env.published.refs)
	}
	ref := env.published.refs[0]
	if ref.SourceType != timeline.SourceActivity {
		t.Errorf("source_type = %q, want activity — a per-sport source type would need a migration per new type", ref.SourceType)
	}
	if ref.SourceID != created.ID {
		t.Errorf("source_id = %q, want the created session %q", ref.SourceID, created.ID)
	}
	if ref.UserID != env.userID {
		t.Errorf("user_id = %q, want %q", ref.UserID, env.userID)
	}
}

// TestContract_BaseOnlyType_CountsAsSnapshotActiveDay: the training
// snapshot's consistency section counts a day with ANY logged session as
// active, so a new base-only type feeds snapshot active-days with no code
// outside its descriptor. This matches the dashboard streak's contract for
// endurance/base-only types; strength_training's completion nuance (the
// dashboard's workoutCompleted gate) doesn't apply to the snapshot — any
// strength row already counted via strength.Sessions, pre-existing and
// unaffected.
func TestContract_BaseOnlyType_CountsAsSnapshotActiveDay(t *testing.T) {
	env := newContractEnv(t)

	w := env.do(t, "POST", "/activities",
		`{"activity_type":"shadowboxing","start_time":"2026-07-20T18:00:00Z","duration_seconds":1800,"name":"Snapshot session"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}

	svc := snapshot.NewService(env.repos.workout, env.repos.exercise, env.repos.activity,
		env.repos.steps, env.repos.bodyweight, env.repos.nutrition, env.repos.user)
	start := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	snap := svc.Build(context.Background(), env.userID, start, end, time.UTC)

	if snap.Consistency.ActiveDays != 1 {
		t.Fatalf("snapshot active days = %d, want 1 (the shadowboxing day); consistency=%+v",
			snap.Consistency.ActiveDays, snap.Consistency)
	}
}

// TestContract_BaseOnlyType_LightsDashboardStreak drives the real dashboard
// handler: any logged non-strength session lights its local day in the
// training streak (streakDates has no per-type switch), so a new base-only
// type counts automatically.
//
// The dashboard clock is not injectable from outside the package, so the
// session is logged "now": created and read milliseconds apart, both fall in
// the same UTC week except across an exactly-interleaved Monday-00:00:00-UTC
// boundary, which is not a realistic flake source.
func TestContract_BaseOnlyType_LightsDashboardStreak(t *testing.T) {
	env := newContractEnv(t)

	now := time.Now().UTC()
	body := fmt.Sprintf(
		`{"activity_type":"shadowboxing","start_time":%q,"duration_seconds":1800,"name":"Streak session"}`,
		now.Format(time.RFC3339))
	w := env.do(t, "POST", "/activities", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}

	w = env.do(t, "GET", "/dashboard/summary?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d; body=%s", w.Code, w.Body.String())
	}
	var env2 struct {
		Data struct {
			Streak struct {
				Weeks              int     `json:"weeks"`
				ActiveDaysThisWeek int     `json:"active_days_this_week"`
				Week               [7]bool `json:"week"`
			} `json:"streak"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env2); err != nil {
		t.Fatalf("decode dashboard: %v; body=%s", err, w.Body.String())
	}
	streak := env2.Data.Streak
	if streak.ActiveDaysThisWeek != 1 {
		t.Fatalf("active_days_this_week = %d, want 1 (shadowboxing day should light up); streak=%+v",
			streak.ActiveDaysThisWeek, streak)
	}
	if streak.Weeks < 1 {
		t.Errorf("weeks = %d, want >= 1", streak.Weeks)
	}
	// Mon→Sun index of the session's UTC day.
	idx := (int(now.Weekday()) + 6) % 7
	if !streak.Week[idx] {
		t.Errorf("week flag for the session's day (index %d) = false; week=%v", idx, streak.Week)
	}
}
