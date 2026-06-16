package user

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
)

// --- in-package fakes ----------------------------------------------------

// fakeLiftSource is an in-package LiftSessionSource returning a fixed slice for
// any user, with the since cutoff applied so the handler-side filtering is
// exercised honestly.
type fakeLiftSource struct {
	sessions []LiftSession
}

func (f *fakeLiftSource) ListCompletedSessionsSince(_ context.Context, _ string, since time.Time) ([]LiftSession, error) {
	var out []LiftSession
	for _, s := range f.sessions {
		if s.PerformedAt.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// fakeRunSource is an in-package RunningSampleSource, same shape as
// fakeLiftSource.
type fakeRunSource struct {
	samples []RunningSample
}

func (f *fakeRunSource) ListRunningSamplesSince(_ context.Context, _ string, since time.Time) ([]RunningSample, error) {
	var out []RunningSample
	for _, s := range f.samples {
		if s.StartTime.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// newStatsFixture builds a discovery handler wired to the given lift/run
// sources plus a real memory user + follow repo, mounted on a chi router.
func newStatsFixture(t *testing.T, lifts LiftSessionSource, runs RunningSampleSource) (chi.Router, Repository, *follow.MemoryRepository) {
	t.Helper()
	userRepo := NewMemoryRepository()
	followRepo := follow.NewMemoryRepository()
	h := NewDiscoveryHandler(userRepo, followRepo, NewFakeAvatarStore(), lifts, runs)
	r := chi.NewRouter()
	h.Mount(r)
	return r, userRepo, followRepo
}

// setTimezone updates a user's timezone via the repo.
func setTimezone(t *testing.T, repo Repository, u *User, tz string) {
	t.Helper()
	u.Timezone = tz
	if err := repo.Update(context.Background(), u); err != nil {
		t.Fatalf("set timezone %q: %v", tz, err)
	}
}

// --- pure-helper tests ---------------------------------------------------

func TestWeekStarts_DenseAndAligned(t *testing.T) {
	loc := time.UTC
	// A Wednesday.
	now := time.Date(2026, 6, 17, 15, 0, 0, 0, time.UTC)
	starts := weekStarts(now, loc)
	if len(starts) != statsWeeks {
		t.Fatalf("len = %d, want %d", len(starts), statsWeeks)
	}
	// Every boundary is a Monday at 00:00.
	for i, s := range starts {
		if s.Weekday() != time.Monday {
			t.Fatalf("starts[%d] weekday = %s, want Monday", i, s.Weekday())
		}
		if s.Hour() != 0 || s.Minute() != 0 || s.Second() != 0 {
			t.Fatalf("starts[%d] = %v, want midnight", i, s)
		}
		if i > 0 {
			if got := starts[i].Sub(starts[i-1]); got != 7*24*time.Hour {
				t.Fatalf("gap %d = %v, want 7d", i, got)
			}
		}
	}
	// The last boundary is the Monday of the week containing now (2026-06-15).
	wantLast := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !starts[statsWeeks-1].Equal(wantLast) {
		t.Fatalf("last = %v, want %v", starts[statsWeeks-1], wantLast)
	}
}

func TestBucketByWeek_DenseZeroFilled(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	starts := weekStarts(now, loc)
	out := bucketByWeek(nil, starts, loc)
	if len(out) != statsWeeks {
		t.Fatalf("len = %d, want %d", len(out), statsWeeks)
	}
	for i, p := range out {
		if p.Value != 0 {
			t.Fatalf("bucket %d value = %v, want 0", i, p.Value)
		}
		if !p.WeekStart.Equal(starts[i]) {
			t.Fatalf("bucket %d week_start = %v, want %v", i, p.WeekStart, starts[i])
		}
	}
}

func TestBucketByWeek_LandsInCorrectWeek(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	starts := weekStarts(now, loc)
	// A sample in the second-to-last week (week index 10), Tuesday.
	target := starts[10].Add(24 * time.Hour)
	out := bucketByWeek([]weekly{{at: target, value: 42}}, starts, loc)
	for i, p := range out {
		want := 0.0
		if i == 10 {
			want = 42
		}
		if p.Value != want {
			t.Fatalf("bucket %d value = %v, want %v", i, p.Value, want)
		}
	}
}

// TestBucketByWeek_TimezoneLocalWeek verifies an instant at Sunday 23:00 LOCAL
// in America/New_York lands in that local week, not the UTC week it spills into
// (Monday 03:00/04:00 UTC).
func TestBucketByWeek_TimezoneLocalWeek(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	// 2026-06-14 is a Sunday. 23:00 local NY (EDT, UTC-4) → 2026-06-15 03:00 UTC,
	// which is a Monday in UTC but still the prior local week in NY.
	instant := time.Date(2026, 6, 14, 23, 0, 0, 0, ny)
	// "now" sits a few weeks later so the instant is inside the window.
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	starts := weekStarts(now, ny)

	out := bucketByWeek([]weekly{{at: instant, value: 5}}, starts, ny)

	// Find which bucket holds the value and assert its boundary is the Monday
	// of the local week that CONTAINS Sunday 2026-06-14 — i.e. Monday
	// 2026-06-08 local.
	wantMonday := time.Date(2026, 6, 8, 0, 0, 0, 0, ny)
	var hit *statsPoint
	for i := range out {
		if out[i].Value != 0 {
			if hit != nil {
				t.Fatalf("value spread across multiple buckets")
			}
			hit = &out[i]
		}
	}
	if hit == nil {
		t.Fatal("no bucket received the value")
	}
	if !hit.WeekStart.Equal(wantMonday) {
		t.Fatalf("local-week bucket = %v, want %v", hit.WeekStart, wantMonday)
	}
	// And explicitly NOT the UTC week (Monday 2026-06-15) the instant spills
	// into when interpreted in UTC.
	utcWeek := time.Date(2026, 6, 15, 0, 0, 0, 0, ny)
	if hit.WeekStart.Equal(utcWeek) {
		t.Fatalf("value landed in the UTC week, expected the local week")
	}
}

// --- handler tests -------------------------------------------------------

// statsResp decodes a profile-stats response body.
func getStats(t *testing.T, r chi.Router, viewer, path string) (*profileStatsDTO, int) {
	t.Helper()
	w := doAs(t, r, viewer, path)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var dto profileStatsDTO
	decodeData(t, w, &dto)
	return &dto, w.Code
}

// TestStats_SelfFullSeries verifies a user reading their own stats gets the
// full dense series with the session landing in the right week.
func TestStats_SelfFullSeries(t *testing.T) {
	now := time.Now().UTC()
	starts := weekStarts(now, time.UTC)
	// A 90-minute session in the most recent week.
	perf := starts[statsWeeks-1].Add(2 * time.Hour)
	lifts := &fakeLiftSource{sessions: []LiftSession{
		{PerformedAt: perf, EndedAt: perf.Add(90 * time.Minute)},
	}}
	runs := &fakeRunSource{samples: []RunningSample{
		{StartTime: perf, DistanceMeters: 5000},
	}}
	r, userRepo, _ := newStatsFixture(t, lifts, runs)

	me := makeUser(t, userRepo, "me@example.com")
	setUsername(t, userRepo, me, "me_handle")
	setTimezone(t, userRepo, me, "UTC")

	dto, code := getStats(t, r, me.ID, "/users/me_handle/stats")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if dto.Locked {
		t.Fatal("self stats should not be locked")
	}
	if len(dto.LiftSessionMinutes) != statsWeeks || len(dto.RunningDistanceMeters) != statsWeeks {
		t.Fatalf("series lengths = %d/%d, want %d each",
			len(dto.LiftSessionMinutes), len(dto.RunningDistanceMeters), statsWeeks)
	}
	if got := dto.LiftSessionMinutes[statsWeeks-1].Value; got != 90 {
		t.Fatalf("lift minutes last week = %v, want 90", got)
	}
	if got := dto.RunningDistanceMeters[statsWeeks-1].Value; got != 5000 {
		t.Fatalf("running meters last week = %v, want 5000", got)
	}
}

// TestStats_NegativeSpanClampedToZero verifies a session whose end precedes its
// start contributes 0, not a negative value.
func TestStats_NegativeSpanClampedToZero(t *testing.T) {
	now := time.Now().UTC()
	starts := weekStarts(now, time.UTC)
	perf := starts[statsWeeks-1].Add(2 * time.Hour)
	lifts := &fakeLiftSource{sessions: []LiftSession{
		{PerformedAt: perf, EndedAt: perf.Add(-30 * time.Minute)},
	}}
	r, userRepo, _ := newStatsFixture(t, lifts, &fakeRunSource{})

	me := makeUser(t, userRepo, "me@example.com")
	setUsername(t, userRepo, me, "me_handle")
	setTimezone(t, userRepo, me, "UTC")

	dto, _ := getStats(t, r, me.ID, "/users/me_handle/stats")
	for i, p := range dto.LiftSessionMinutes {
		if p.Value != 0 {
			t.Fatalf("bucket %d value = %v, want 0 (negative span clamped)", i, p.Value)
		}
	}
}

// TestStats_EndlessWorkoutsAllZero verifies that when the source returns no
// completed sessions (end-less workouts are excluded upstream), the lift series
// is all zero but still dense.
func TestStats_EndlessWorkoutsAllZero(t *testing.T) {
	r, userRepo, _ := newStatsFixture(t, &fakeLiftSource{}, &fakeRunSource{})
	me := makeUser(t, userRepo, "me@example.com")
	setUsername(t, userRepo, me, "me_handle")
	setTimezone(t, userRepo, me, "UTC")

	dto, _ := getStats(t, r, me.ID, "/users/me_handle/stats")
	if len(dto.LiftSessionMinutes) != statsWeeks {
		t.Fatalf("len = %d, want %d", len(dto.LiftSessionMinutes), statsWeeks)
	}
	for i, p := range dto.LiftSessionMinutes {
		if p.Value != 0 {
			t.Fatalf("bucket %d = %v, want 0", i, p.Value)
		}
	}
}

// TestStats_PrivacyMatrix asserts the gate mirrors the activity feed: self and
// accepted-follower get the full series; a non-follower is locked with empty
// series. There is no public bypass.
func TestStats_PrivacyMatrix(t *testing.T) {
	now := time.Now().UTC()
	starts := weekStarts(now, time.UTC)
	perf := starts[statsWeeks-1].Add(2 * time.Hour)
	lifts := &fakeLiftSource{sessions: []LiftSession{
		{PerformedAt: perf, EndedAt: perf.Add(60 * time.Minute)},
	}}
	r, userRepo, followRepo := newStatsFixture(t, lifts, &fakeRunSource{})
	ctx := context.Background()

	target := makeUser(t, userRepo, "t@example.com")
	setUsername(t, userRepo, target, "target")
	setTimezone(t, userRepo, target, "UTC")
	follower := makeUser(t, userRepo, "f@example.com")
	setUsername(t, userRepo, follower, "follower")
	stranger := makeUser(t, userRepo, "s@example.com")
	setUsername(t, userRepo, stranger, "stranger")

	// follower → target accepted.
	if _, err := followRepo.Request(ctx, follower.ID, target.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := followRepo.Accept(ctx, target.ID, follower.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// self → full.
	selfDTO, _ := getStats(t, r, target.ID, "/users/target/stats")
	if selfDTO.Locked {
		t.Fatal("self should not be locked")
	}
	if selfDTO.LiftSessionMinutes[statsWeeks-1].Value != 60 {
		t.Fatalf("self last week = %v, want 60", selfDTO.LiftSessionMinutes[statsWeeks-1].Value)
	}

	// accepted follower → full.
	folDTO, _ := getStats(t, r, follower.ID, "/users/target/stats")
	if folDTO.Locked {
		t.Fatal("accepted follower should not be locked")
	}
	if folDTO.LiftSessionMinutes[statsWeeks-1].Value != 60 {
		t.Fatalf("follower last week = %v, want 60", folDTO.LiftSessionMinutes[statsWeeks-1].Value)
	}

	// stranger (non-follower) → locked, empty series. No public bypass.
	strDTO, _ := getStats(t, r, stranger.ID, "/users/target/stats")
	if !strDTO.Locked {
		t.Fatal("non-follower must be locked (no public bypass)")
	}
	if len(strDTO.LiftSessionMinutes) != 0 || len(strDTO.RunningDistanceMeters) != 0 {
		t.Fatalf("locked series should be empty, got %d/%d",
			len(strDTO.LiftSessionMinutes), len(strDTO.RunningDistanceMeters))
	}
}

// TestStats_RequestedNotEnoughToUnlock asserts a pending (requested, not yet
// accepted) follow does NOT unlock — only RelationshipFollowing does.
func TestStats_RequestedNotEnoughToUnlock(t *testing.T) {
	r, userRepo, followRepo := newStatsFixture(t, &fakeLiftSource{}, &fakeRunSource{})
	ctx := context.Background()

	target := makeUser(t, userRepo, "t@example.com")
	setUsername(t, userRepo, target, "target")
	setTimezone(t, userRepo, target, "UTC")
	viewer := makeUser(t, userRepo, "v@example.com")
	setUsername(t, userRepo, viewer, "viewer")

	// viewer → target pending (requested), not accepted.
	if _, err := followRepo.Request(ctx, viewer.ID, target.ID); err != nil {
		t.Fatalf("request: %v", err)
	}

	dto, _ := getStats(t, r, viewer.ID, "/users/target/stats")
	if !dto.Locked {
		t.Fatal("a pending (requested) relationship must stay locked")
	}
}

// TestStats_UnknownUsername404 verifies an unknown handle 404s.
func TestStats_UnknownUsername404(t *testing.T) {
	r, userRepo, _ := newStatsFixture(t, &fakeLiftSource{}, &fakeRunSource{})
	me := makeUser(t, userRepo, "me@example.com")
	w := doAs(t, r, me.ID, "/users/ghost/stats")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
}
