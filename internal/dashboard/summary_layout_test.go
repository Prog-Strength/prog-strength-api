package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
)

// dataEnvelope drives the summary endpoint and returns the decoded `layout`
// list plus the `data` object as raw section messages (layout key included), so
// a test can distinguish an ABSENT key (tile not in layout) from a
// present-but-null key (tile enabled, no data). Asserts 200.
func dataEnvelope(t *testing.T, r *chi.Mux, userID, query string) ([]string, map[string]json.RawMessage) {
	t.Helper()
	rec := get(t, r, userID, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	var layout []string
	if err := json.Unmarshal(env.Data["layout"], &layout); err != nil {
		t.Fatalf("decode layout: %v; body=%s", err, rec.Body.String())
	}
	return layout, env.Data
}

func assertKeysPresent(t *testing.T, data map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := data[k]; !ok {
			t.Errorf("key %q absent, want present", k)
		}
	}
}

func assertKeysAbsent(t *testing.T, data map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := data[k]; ok {
			t.Errorf("key %q present, want absent", k)
		}
	}
}

func TestSummary_DefaultLayout_NoWhoop(t *testing.T) {
	r, _, userID := newTestEnv(t)

	layout, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	want := []string{"running", "lifting", "steps", "nutrition", "bodyweight", "streak"}
	if !equalStrs(layout, want) {
		t.Errorf("layout = %v, want %v", layout, want)
	}
	assertKeysPresent(t, data, "running", "lifting", "steps", "nutrition", "bodyweight", "streak")
	assertKeysAbsent(t, data, "recovery", "walking", "cycling", "hiking")
	// Blood pressure is a catalog tile but NOT part of the default layout — a
	// fresh user should not land on a blood-pressure card they never opted into.
	if indexOf(layout, string(TileBloodPressure)) >= 0 {
		t.Errorf("default layout must not contain %q; got %v", TileBloodPressure, layout)
	}
	assertKeysAbsent(t, data, "blood_pressure")
}

func TestSummary_DefaultLayout_WithConnectedWhoop(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)

	layout, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	want := []string{"running", "lifting", "steps", "nutrition", "bodyweight", "recovery", "streak"}
	if !equalStrs(layout, want) {
		t.Errorf("layout = %v, want %v", layout, want)
	}
	// recovery must sit before streak.
	recIdx, streakIdx := indexOf(layout, "recovery"), indexOf(layout, "streak")
	if recIdx < 0 || streakIdx < 0 || recIdx > streakIdx {
		t.Errorf("recovery (%d) must precede streak (%d)", recIdx, streakIdx)
	}
	assertKeysPresent(t, data, "recovery")
}

func TestSummary_StoredLayout_GatesSections(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileRunning, TileStreak}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	layout, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	if !equalStrs(layout, []string{"running", "streak"}) {
		t.Errorf("layout = %v, want [running streak]", layout)
	}
	assertKeysPresent(t, data, "running", "streak")
	assertKeysAbsent(t, data, "nutrition", "bodyweight", "lifting", "steps", "walking", "cycling", "hiking", "recovery")
}

// TestSummary_StreakUnchangedWhenStepsDisabled proves the shared-read trap: the
// streak credits goal-meeting step days from the ungated steps read, so
// disabling the Steps TILE must not change the streak object.
func TestSummary_StreakUnchangedWhenStepsDisabled(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	loc := time.UTC
	now := testNow.In(loc)
	monday := localWeekStart(now, loc)

	// A passive step day that MEETS the goal within the current week → lights a
	// streak day only through the step-goal path.
	if _, err := rp.steps.UpsertGoal(context.Background(), steps.Goal{UserID: userID, Goal: 8000}, now); err != nil {
		t.Fatalf("steps goal: %v", err)
	}
	seedSteps(t, rp, userID, monday.AddDate(0, 0, 2).Format("2006-01-02"), 9000)

	// Layout with Steps enabled.
	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileSteps, TileStreak}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}
	_, withSteps := dataEnvelope(t, r, userID, "?timezone=UTC")

	// Layout with Steps disabled.
	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileStreak}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}
	_, withoutSteps := dataEnvelope(t, r, userID, "?timezone=UTC")

	if !bytes.Equal(withSteps["streak"], withoutSteps["streak"]) {
		t.Errorf("streak changed when Steps tile disabled:\n with steps: %s\n without:    %s",
			withSteps["streak"], withoutSteps["streak"])
	}
	// Sanity: the streak actually lit the goal-met day (non-trivial equality).
	var streak StreakSection
	if err := json.Unmarshal(withSteps["streak"], &streak); err != nil {
		t.Fatalf("decode streak: %v", err)
	}
	if !streak.Week[2] {
		t.Error("Wednesday not active in streak; step-goal path did not fire (test premise broken)")
	}
}

// TestSummary_StreakUsesWorkoutsWhenLiftingDisabled is the mirror of the steps
// trap: the streak's completion path needs hydrated workouts, so hydration is
// gated on lifting OR streak — not lifting alone. A completed workout on a day
// in the current week must still light the streak when the Lifting tile is
// disabled but Streak is enabled. This fails if hydration were gated on
// lifting-only.
func TestSummary_StreakUsesWorkoutsWhenLiftingDisabled(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	loc := time.UTC
	now := testNow.In(loc)

	// A completed workout today (Wed 2026-06-17). seedWorkout sets EndedAt and a
	// logged set, so it satisfies the streak's completion check.
	seedWorkout(t, rp, userID, now.Add(-2*time.Hour), "barbell-bench-press", 185, 5)

	// Lifting DISABLED, Streak ENABLED.
	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileStreak}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	_, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	assertKeysAbsent(t, data, "lifting")
	var streak StreakSection
	if err := json.Unmarshal(data["streak"], &streak); err != nil {
		t.Fatalf("decode streak: %v", err)
	}
	idx := weekdayIndex(now, loc)
	if !streak.Week[idx] {
		t.Errorf("streak week[%d] not active; completed workout did not light the streak with lifting disabled", idx)
	}
	if streak.ActiveDaysThisWeek < 1 {
		t.Errorf("active_days_this_week = %d, want >= 1 (workout hydration must run when streak is enabled)", streak.ActiveDaysThisWeek)
	}
}

// failingLayoutRepo returns a non-ErrLayoutNotFound error from Get, exercising
// the resolveLayout fallback-to-default path. Upsert is a no-op.
type failingLayoutRepo struct{}

func (failingLayoutRepo) Get(ctx context.Context, userID string) (Layout, error) {
	return Layout{}, errors.New("boom")
}
func (failingLayoutRepo) Upsert(ctx context.Context, userID string, tileIDs []TileID) error {
	return nil
}

func TestSummary_LayoutReadFailure_FallsBackToDefault(t *testing.T) {
	_, rp, userID := newTestEnv(t)

	// Rebuild the handler with a failing layout repo, reusing the real repos.
	h := NewHandler(rp.activity, rp.workout, rp.exercise, rp.steps, rp.nutrition, rp.bodyweight, rp.bloodPressure, rp.user, rp.whoopConn, rp.whoopRec, failingLayoutRepo{}, testRecoveryEngine())
	h.now = func() time.Time { return testNow }
	r2 := chi.NewRouter()
	h.Mount(r2)

	layout, _ := dataEnvelope(t, r2, userID, "?timezone=UTC")

	// No whoop connection → default without recovery.
	want := []string{"running", "lifting", "steps", "nutrition", "bodyweight", "streak"}
	if !equalStrs(layout, want) {
		t.Errorf("layout = %v, want default %v", layout, want)
	}
}

// TestSummary_EnabledButNoData_SerializesNull asserts that a tile enabled in the
// layout but with no underlying data serializes as JSON null (present key, null
// value) rather than being absent. A brand-new user has no nutrition rows.
func TestSummary_EnabledButNoData_SerializesNull(t *testing.T) {
	r, _, userID := newTestEnv(t)

	_, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	raw, ok := data["nutrition"]
	if !ok {
		t.Fatal("nutrition key absent, want present (it is in the default layout)")
	}
	if string(raw) != "null" {
		t.Errorf("nutrition = %s, want null (enabled but no data)", raw)
	}
}

// --- small helpers ----------------------------------------------------

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexOf(s []string, v string) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// seedWhoopConnected upserts a CONNECTED Whoop connection so
// buildRecoverySection passes its gate (a fresh Upsert sets status=connected,
// per the Repository docs).
func seedWhoopConnected(t *testing.T, rp *repos, userID string) {
	t.Helper()
	tokens := whoopconn.TokenBundle{
		AccessTokenEnc:    []byte("a"),
		AccessTokenNonce:  []byte("n"),
		RefreshTokenEnc:   []byte("r"),
		RefreshTokenNonce: []byte("n"),
		ExpiresAt:         testNow.Add(time.Hour),
	}
	if err := rp.whoopConn.Upsert(context.Background(), userID, 12345, tokens, "read:recovery", testNow); err != nil {
		t.Fatalf("whoop upsert: %v", err)
	}
}

// TestSummary_FamilyTileAlone_YieldsRecoverySection pins the SOW's re-gate: a
// layout containing ONLY hrv_balance (no recovery tile) must still produce a
// populated "recovery" section — and no "hrv_balance" key. This is the first
// place the response's section-key set is deliberately not a subset of layout.
func TestSummary_FamilyTileAlone_YieldsRecoverySection(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)

	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileHRVBalance}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	layout, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	if !equalStrs(layout, []string{"hrv_balance"}) {
		t.Errorf("layout = %v, want [hrv_balance]", layout)
	}
	assertKeysPresent(t, data, "recovery")
	assertKeysAbsent(t, data, "hrv_balance")
	if string(data["recovery"]) == "null" {
		t.Error("recovery = null, want a populated section for a connected user")
	}
}

// TestSummary_MultipleFamilyTiles_OneRecoverySection asserts the section is
// built and emitted exactly once under the "recovery" key no matter how many
// family tiles are on the layout — no per-tile section keys.
func TestSummary_MultipleFamilyTiles_OneRecoverySection(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)

	ids := []TileID{TileRecovery, TileMorningVitals, TileRecoveryLog}
	if err := rp.layout.Upsert(context.Background(), userID, ids); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	layout, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	if !equalStrs(layout, []string{"recovery", "morning_vitals", "recovery_log"}) {
		t.Errorf("layout = %v, want [recovery morning_vitals recovery_log]", layout)
	}
	assertKeysPresent(t, data, "recovery")
	assertKeysAbsent(t, data, "morning_vitals", "recovery_log", "hrv_balance", "recovery_trend")
}

// TestSummary_NoFamilyTile_NoRecoverySection asserts the inverse: with no
// recovery-family tile enabled, the "recovery" key is absent entirely.
func TestSummary_NoFamilyTile_NoRecoverySection(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)

	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileRunning, TileStreak}); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	_, data := dataEnvelope(t, r, userID, "?timezone=UTC")

	assertKeysAbsent(t, data, "recovery", "hrv_balance", "morning_vitals", "recovery_trend", "recovery_log")
}
