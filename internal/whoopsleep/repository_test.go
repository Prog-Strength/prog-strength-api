package whoopsleep

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

func iptr(i int64) *int64     { return &i }
func fptr(f float64) *float64 { return &f }

// newRepo builds a repository over an ephemeral DB with the users the tests
// reference already present: user_whoop_sleep carries a real foreign key to
// users, so a row cannot exist for a user that does not.
func newRepo(t *testing.T, userIDs ...string) *SQLiteRepository {
	t.Helper()
	database := dbtest.New(t)
	for _, uid := range userIDs {
		seedUser(t, database, uid)
	}
	return NewSQLiteRepository(database)
}

func seedUser(t *testing.T, database *sql.DB, userID string) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := database.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES (?, ?, ?, 'lb', ?, ?)`, userID, userID+"@example.com", userID, now, now)
	if err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
}

// night is a fully-populated scored record, so a test can vary one field
// without restating twenty.
func night(userID, whoopID, date string) Entry {
	return Entry{
		UserID:         userID,
		WhoopSleepID:   whoopID,
		Date:           date,
		IsNap:          false,
		StartedAt:      date + "T06:40:00-06:00",
		EndedAt:        date + "T14:15:00-06:00",
		TimezoneOffset: "-06:00",
		ScoreState:     "SCORED",

		InBedMilli:         iptr(27300000),
		AwakeMilli:         iptr(1800000),
		NoDataMilli:        iptr(0),
		LightSleepMilli:    iptr(12000000),
		SlowWaveSleepMilli: iptr(7500000),
		REMSleepMilli:      iptr(6000000),
		SleepCycleCount:    iptr(5),
		DisturbanceCount:   iptr(9),

		NeedBaselineMilli:      iptr(28000000),
		NeedFromSleepDebtMilli: iptr(1200000),
		NeedFromStrainMilli:    iptr(600000),
		NeedFromNapMilli:       iptr(-300000),

		RespiratoryRate: fptr(15.2),
		PerformancePct:  fptr(92.5),
		ConsistencyPct:  fptr(71),
		EfficiencyPct:   fptr(88.25),
	}
}

// --- Upsert: replace, don't duplicate; preserve id + created_at --------

func TestUpsert_ReplacesNotDuplicates(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")

	t0 := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, night("u1", "sleep-a", "2026-06-14"), t0); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	first := got[0]
	if first.ID == "" {
		t.Fatal("upsert did not populate ID")
	}
	if *first.SlowWaveSleepMilli != 7500000 || *first.NeedFromNapMilli != -300000 {
		t.Fatalf("unexpected first row: %+v", first)
	}

	// Re-upsert the same (user_id, whoop_sleep_id) with new metrics later.
	t1 := t0.Add(2 * time.Hour)
	updated := night("u1", "sleep-a", "2026-06-14")
	updated.ScoreState = "SCORED"
	updated.SlowWaveSleepMilli = iptr(9000000)
	updated.PerformancePct = fptr(97)
	updated.DisturbanceCount = iptr(3)
	if err = repo.Upsert(ctx, updated, t1); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err = repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row after re-upsert, got %d: %+v", len(got), got)
	}
	second := got[0]

	if *second.SlowWaveSleepMilli != 9000000 || *second.PerformancePct != 97 || *second.DisturbanceCount != 3 {
		t.Errorf("metrics not replaced: %+v", second)
	}
	if second.ID != first.ID {
		t.Errorf("id should be preserved: first=%s second=%s", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at should be preserved: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at should advance: first=%v second=%v", first.UpdatedAt, second.UpdatedAt)
	}
	if !second.UpdatedAt.Equal(t1.UTC()) {
		t.Errorf("updated_at = %v, want %v", second.UpdatedAt, t1.UTC())
	}
}

// TestUpsert_NapAndNightOnSameDateBothPersist is the case the whole keying
// decision exists for: (user_id, date) is NOT unique, so a nap and the night
// it followed must both survive.
func TestUpsert_NapAndNightOnSameDateBothPersist(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)

	main := night("u1", "sleep-night", "2026-06-14")
	main.StartedAt = "2026-06-13T22:40:00-06:00"
	main.EndedAt = "2026-06-14T06:15:00-06:00"
	nap := night("u1", "sleep-nap", "2026-06-14")
	nap.IsNap = true
	nap.StartedAt = "2026-06-14T14:00:00-06:00"
	nap.EndedAt = "2026-06-14T14:45:00-06:00"
	nap.InBedMilli = iptr(2700000)

	for _, e := range []Entry{main, nap} {
		if err := repo.Upsert(ctx, e, now); err != nil {
			t.Fatalf("upsert %s: %v", e.WhoopSleepID, err)
		}
	}

	got, err := repo.ListRange(ctx, "u1", "2026-06-14", "2026-06-14")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both records on 2026-06-14, got %d: %+v", len(got), got)
	}
	// date DESC, ended_at DESC: the nap ended later, so it sorts first.
	if got[0].WhoopSleepID != "sleep-nap" || !got[0].IsNap {
		t.Errorf("first row = %+v, want the nap (later ended_at)", got[0])
	}
	if got[1].WhoopSleepID != "sleep-night" || got[1].IsNap {
		t.Errorf("second row = %+v, want the night", got[1])
	}
}

// --- Nullable round-trip -----------------------------------------------

// TestUpsert_UnscoredRoundTripsAsNil pins that a PENDING record is stored with
// its start, end, and nothing else, so the row already exists when WHOOP
// scores it.
func TestUpsert_UnscoredRoundTripsAsNil(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	pending := Entry{
		UserID: "u1", WhoopSleepID: "sleep-pending", Date: "2026-06-01",
		StartedAt:      "2026-05-31T23:10:00-06:00",
		EndedAt:        "2026-06-01T07:05:00-06:00",
		TimezoneOffset: "-06:00",
		ScoreState:     "PENDING",
	}
	if err := repo.Upsert(ctx, pending, now); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}

	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	e := got[0]
	if e.ScoreState != "PENDING" || e.StartedAt != pending.StartedAt || e.EndedAt != pending.EndedAt {
		t.Errorf("non-score fields not round-tripped: %+v", e)
	}
	if e.InBedMilli != nil || e.AwakeMilli != nil || e.NoDataMilli != nil ||
		e.LightSleepMilli != nil || e.SlowWaveSleepMilli != nil || e.REMSleepMilli != nil ||
		e.SleepCycleCount != nil || e.DisturbanceCount != nil {
		t.Errorf("stage fields should read back nil: %+v", e)
	}
	if e.NeedBaselineMilli != nil || e.NeedFromSleepDebtMilli != nil ||
		e.NeedFromStrainMilli != nil || e.NeedFromNapMilli != nil {
		t.Errorf("need fields should read back nil: %+v", e)
	}
	if e.RespiratoryRate != nil || e.PerformancePct != nil ||
		e.ConsistencyPct != nil || e.EfficiencyPct != nil {
		t.Errorf("score floats should read back nil: %+v", e)
	}
}

// TestUpsert_ScoredRoundTripsEveryField guards against a column/scan
// misalignment: with twenty near-identical INTEGER columns, a swapped pair is
// invisible unless every value is distinct and checked.
func TestUpsert_ScoredRoundTripsEveryField(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	want := night("u1", "sleep-full", "2026-06-14")
	if err := repo.Upsert(ctx, want, now); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.WhoopSleepID != want.WhoopSleepID || got.Date != want.Date ||
		got.IsNap != want.IsNap || got.StartedAt != want.StartedAt ||
		got.EndedAt != want.EndedAt || got.TimezoneOffset != want.TimezoneOffset ||
		got.ScoreState != want.ScoreState {
		t.Errorf("identity fields mismatch: got %+v want %+v", got, want)
	}
	ints := []struct {
		name      string
		got, want *int64
	}{
		{"in_bed_milli", got.InBedMilli, want.InBedMilli},
		{"awake_milli", got.AwakeMilli, want.AwakeMilli},
		{"no_data_milli", got.NoDataMilli, want.NoDataMilli},
		{"light_sleep_milli", got.LightSleepMilli, want.LightSleepMilli},
		{"slow_wave_sleep_milli", got.SlowWaveSleepMilli, want.SlowWaveSleepMilli},
		{"rem_sleep_milli", got.REMSleepMilli, want.REMSleepMilli},
		{"sleep_cycle_count", got.SleepCycleCount, want.SleepCycleCount},
		{"disturbance_count", got.DisturbanceCount, want.DisturbanceCount},
		{"need_baseline_milli", got.NeedBaselineMilli, want.NeedBaselineMilli},
		{"need_from_sleep_debt_milli", got.NeedFromSleepDebtMilli, want.NeedFromSleepDebtMilli},
		{"need_from_strain_milli", got.NeedFromStrainMilli, want.NeedFromStrainMilli},
		{"need_from_nap_milli", got.NeedFromNapMilli, want.NeedFromNapMilli},
	}
	for _, c := range ints {
		if c.got == nil || *c.got != *c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, *c.want)
		}
	}
	floats := []struct {
		name      string
		got, want *float64
	}{
		{"respiratory_rate", got.RespiratoryRate, want.RespiratoryRate},
		{"performance_pct", got.PerformancePct, want.PerformancePct},
		{"consistency_pct", got.ConsistencyPct, want.ConsistencyPct},
		{"efficiency_pct", got.EfficiencyPct, want.EfficiencyPct},
	}
	for _, c := range floats {
		if c.got == nil || *c.got != *c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, *c.want)
		}
	}
}

// --- ListRange: windowing + ordering + isolation -----------------------

func TestListRange_WindowOrderingAndIsolation(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1", "u2")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	seed := func(user, date string) {
		e := night(user, user+"-"+date, date)
		if err := repo.Upsert(ctx, e, now); err != nil {
			t.Fatalf("seed %s/%s: %v", user, date, err)
		}
	}
	for _, d := range []string{"2026-06-10", "2026-06-11", "2026-06-12", "2026-06-13"} {
		seed("u1", d)
	}
	seed("u2", "2026-06-11")

	datesOf := func(es []Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Date
		}
		return out
	}

	// Inclusive window 11..13, DESC.
	got, err := repo.ListRange(ctx, "u1", "2026-06-11", "2026-06-13")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-13", "2026-06-12", "2026-06-11"}; !equalStrings(datesOf(got), want) {
		t.Errorf("windowed DESC = %v, want %v", datesOf(got), want)
	}

	// Unbounded since → 11, 10 DESC.
	got, err = repo.ListRange(ctx, "u1", "", "2026-06-11")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-11", "2026-06-10"}; !equalStrings(datesOf(got), want) {
		t.Errorf("unbounded-since = %v, want %v", datesOf(got), want)
	}

	// Unbounded until → 13, 12 DESC.
	got, err = repo.ListRange(ctx, "u1", "2026-06-12", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-13", "2026-06-12"}; !equalStrings(datesOf(got), want) {
		t.Errorf("unbounded-until = %v, want %v", datesOf(got), want)
	}

	// Fully unbounded → all four for u1.
	got, err = repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("fully unbounded should return 4 rows, got %d", len(got))
	}

	// User isolation: u2 sees only their own row.
	got, err = repo.ListRange(ctx, "u2", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Date != "2026-06-11" {
		t.Errorf("u2 should see only their own row, got %+v", got)
	}
}

// TestListRange_OrdersByEndedAtWithinADate pins the secondary sort. Several
// records can share a date, so date alone is not a total order and the result
// would otherwise depend on insertion order.
func TestListRange_OrdersByEndedAtWithinADate(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	early := night("u1", "early", "2026-06-14")
	early.EndedAt = "2026-06-14T06:15:00-06:00"
	late := night("u1", "late", "2026-06-14")
	late.EndedAt = "2026-06-14T18:30:00-06:00"
	// Insert the later record first, so insertion order can't accidentally
	// produce the right answer.
	for _, e := range []Entry{late, early} {
		if err := repo.Upsert(ctx, e, now); err != nil {
			t.Fatalf("seed %s: %v", e.WhoopSleepID, err)
		}
	}

	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].WhoopSleepID != "late" || got[1].WhoopSleepID != "early" {
		t.Errorf("order = %s, %s; want late, early", got[0].WhoopSleepID, got[1].WhoopSleepID)
	}
}

// TestListRange_OrdersByWhoopSleepIDWhenDateAndEndedAtTie pins the last
// tie-breaker. date + ended_at is still not a total order, and SQLite is free to
// return equal-keyed rows in either order between queries; whoop_sleep_id is
// unique per user, so appending it makes the sort reproducible. Consumers that
// resolve a night from a same-date group depend on that.
func TestListRange_OrdersByWhoopSleepIDWhenDateAndEndedAtTie(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	a := night("u1", "aaa", "2026-06-14")
	a.EndedAt = "2026-06-14T06:15:00-06:00"
	b := night("u1", "bbb", "2026-06-14")
	b.EndedAt = a.EndedAt
	// Seeded in an order SQLite demonstrably does not reproduce for this pair
	// without the whoop_sleep_id term, so the assertion below fails if it is
	// dropped rather than passing on whatever the planner happened to emit.
	for _, e := range []Entry{b, a} {
		if err := repo.Upsert(ctx, e, now); err != nil {
			t.Fatalf("seed %s: %v", e.WhoopSleepID, err)
		}
	}

	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].WhoopSleepID != "bbb" || got[1].WhoopSleepID != "aaa" {
		t.Errorf("order = %s, %s; want bbb, aaa", got[0].WhoopSleepID, got[1].WhoopSleepID)
	}
}

// --- DeleteByWhoopSleepID: removes match, idempotent when absent ---------

func TestDeleteByWhoopSleepID(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, "u1", "u2")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, night("u1", "sleep-x", "2026-06-14"), now); err != nil {
		t.Fatalf("seed u1: %v", err)
	}
	if err := repo.Upsert(ctx, night("u1", "sleep-y", "2026-06-15"), now); err != nil {
		t.Fatalf("seed u1 second: %v", err)
	}
	// Same WHOOP UUID under a different user: a delete must never cross users.
	if err := repo.Upsert(ctx, night("u2", "sleep-x", "2026-06-14"), now); err != nil {
		t.Fatalf("seed u2: %v", err)
	}

	// Unknown UUID → no error, nothing removed (idempotent webhook delete).
	if err := repo.DeleteByWhoopSleepID(ctx, "u1", "sleep-absent"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows should survive a non-matching delete, got %d", len(got))
	}

	// Matching delete removes exactly the one row.
	if err = repo.DeleteByWhoopSleepID(ctx, "u1", "sleep-x"); err != nil {
		t.Fatalf("delete match: %v", err)
	}
	got, err = repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].WhoopSleepID != "sleep-y" {
		t.Errorf("delete removed the wrong rows: %+v", got)
	}

	// u2's identically-keyed row is untouched.
	other, err := repo.ListRange(ctx, "u2", "", "")
	if err != nil {
		t.Fatalf("list u2: %v", err)
	}
	if len(other) != 1 {
		t.Errorf("delete leaked across users, u2 rows = %d", len(other))
	}

	// Redelivery of the same event → still no error.
	if err = repo.DeleteByWhoopSleepID(ctx, "u1", "sleep-x"); err != nil {
		t.Errorf("idempotent delete: want nil, got %v", err)
	}
}

func equalStrings(a, b []string) bool {
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
