package weather

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// The detail tables the anti-join unions. Named here so a test can walk all
// five and catch a table dropped from outdoorDetailsUnion.
var enduranceDetailTables = []string{
	"activity_run_details",
	"activity_walk_details",
	"activity_cycle_details",
	"activity_hike_details",
	"activity_other_details",
}

func newActivityWeatherRepo(t *testing.T) (*SQLiteActivityWeatherRepository, *sql.DB) {
	t.Helper()
	database := dbtest.New(t)
	mustInsertUser(t, database, "u1")
	return NewSQLiteActivityWeatherRepository(database), database
}

// seedActivity inserts a live activity plus the detail row that actually
// carries `environment` — it lives per activity type, not on activities.
func seedActivity(t *testing.T, db *sql.DB, activityID string, start time.Time, table, environment string) {
	t.Helper()
	mustInsertActivity(t, db, activityID, "u1", start)
	if _, err := db.Exec(`
		INSERT INTO `+table+` (activity_id, distance_meters, raw_distance_meters, environment)
		VALUES (?, 10000, 10000, ?)
	`, activityID, environment); err != nil {
		t.Fatalf("insert %s for %q: %v", table, activityID, err)
	}
}

func seedOutdoorRun(t *testing.T, db *sql.DB, activityID string, start time.Time) {
	t.Helper()
	seedActivity(t, db, activityID, start, "activity_run_details", "outdoor")
}

// mustInsertTrackpoint takes lat/lon as any so a test can seed a genuinely
// unpositioned point with nil, which is what a treadmill-shaped track or a
// dropped GPS fix looks like on disk.
func mustInsertTrackpoint(t *testing.T, db *sql.DB, activityID string, sequence int, lat, lon any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, latitude, longitude)
		VALUES (?, ?, ?, 0, ?, ?)
	`, activityID, sequence, sequence, lat, lon); err != nil {
		t.Fatalf("insert trackpoint %d for %q: %v", sequence, activityID, err)
	}
}

func mustSoftDelete(t *testing.T, db *sql.DB, activityID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE activities SET deleted_at = ? WHERE id = ?`,
		time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC), activityID); err != nil {
		t.Fatalf("soft delete %q: %v", activityID, err)
	}
}

func ptrFloat(v float64) *float64 { return &v }

func okRow(activityID string) ActivityWeather {
	return ActivityWeather{
		ActivityID: activityID,
		Status:     ActivityStatusOK,
		Lat:        ptrFloat(39.7392),
		Lon:        ptrFloat(-104.9847),
		Observation: &Observation{
			ObservedAt: time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC),
			TempC:      31.8,
			FeelsLikeC: 33.4,
			DewPointC:  12.5,
			Humidity:   28,
			WindKMH:    18.36,
			WindDeg:    210,
			PrecipMM:   1.25,
			Condition:  "Clear",
			Icon:       "01d",
		},
		FetchedAt: time.Date(2026, 8, 9, 15, 4, 5, 0, time.UTC),
	}
}

func TestActivityWeatherGetAbsentRowReturnsNilNil(t *testing.T) {
	repo, _ := newActivityWeatherRepo(t)

	row, err := repo.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get on an absent row: %v", err)
	}
	if row != nil {
		t.Fatalf("Get on an absent row = %+v, want nil (absence is an answer, not an error)", row)
	}
}

func TestActivityWeatherPutGetRoundTripsOKRow(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	want := okRow("a1")
	if err := repo.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := repo.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get after Put = nil, want the stored row")
	}
	if got.Status != ActivityStatusOK {
		t.Errorf("Status = %q, want %q", got.Status, ActivityStatusOK)
	}
	if got.Lat == nil || got.Lon == nil || *got.Lat != *want.Lat || *got.Lon != *want.Lon {
		t.Errorf("coordinates = %v/%v, want %v/%v", got.Lat, got.Lon, want.Lat, want.Lon)
	}
	if !got.FetchedAt.Equal(want.FetchedAt) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, want.FetchedAt)
	}
	if got.Observation == nil {
		t.Fatal("Observation = nil on an ok row, want the reading")
	}
	if !got.Observation.ObservedAt.Equal(want.Observation.ObservedAt) {
		t.Errorf("ObservedAt = %v, want %v", got.Observation.ObservedAt, want.Observation.ObservedAt)
	}
	// Compare the rest by value: ObservedAt is the only field time.Time
	// equality would trip on.
	gotReading, wantReading := *got.Observation, *want.Observation
	gotReading.ObservedAt, wantReading.ObservedAt = time.Time{}, time.Time{}
	if gotReading != wantReading {
		t.Errorf("reading = %+v, want %+v", gotReading, wantReading)
	}
}

func TestActivityWeatherPutGetRoundTripsNoCoordinatesRow(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	fetchedAt := time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC)
	if err := repo.Put(ctx, ActivityWeather{
		ActivityID: "a1",
		Status:     ActivityStatusNoCoordinates,
		FetchedAt:  fetchedAt,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := repo.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get after Put = nil, want the stored row")
	}
	if got.Status != ActivityStatusNoCoordinates {
		t.Errorf("Status = %q, want %q", got.Status, ActivityStatusNoCoordinates)
	}
	if got.Observation != nil {
		t.Errorf("Observation = %+v on a no_coordinates row, want nil", got.Observation)
	}
	if got.Lat != nil || got.Lon != nil {
		t.Errorf("coordinates = %v/%v, want nil/nil", got.Lat, got.Lon)
	}
	if !got.FetchedAt.Equal(fetchedAt) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, fetchedAt)
	}

	// The reading columns must be NULL on disk, not zeroes: a 0.0 °C stored
	// for an activity we never measured would render as a fact.
	var temp, wind sql.NullFloat64
	var observedAt sql.NullTime
	if err := database.QueryRow(
		`SELECT temp_c, wind_kmh, observed_at FROM activity_weather WHERE activity_id = 'a1'`,
	).Scan(&temp, &wind, &observedAt); err != nil {
		t.Fatalf("read raw columns: %v", err)
	}
	if temp.Valid || wind.Valid || observedAt.Valid {
		t.Errorf("reading columns valid = %v/%v/%v, want all NULL", temp.Valid, wind.Valid, observedAt.Valid)
	}
}

// Put upserts because --retry-unavailable clears rows and a re-import reuses
// the activity id: a second write is a re-capture, not a collision.
func TestActivityWeatherPutUpsertsOverAnExistingRow(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	if err := repo.Put(ctx, ActivityWeather{
		ActivityID: "a1",
		Status:     ActivityStatusUnavailable,
		Lat:        ptrFloat(39.7392),
		Lon:        ptrFloat(-104.9847),
		FetchedAt:  time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Put unavailable: %v", err)
	}
	if err := repo.Put(ctx, okRow("a1")); err != nil {
		t.Fatalf("Put ok over unavailable: %v", err)
	}

	got, err := repo.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != ActivityStatusOK || got.Observation == nil {
		t.Fatalf("row after re-Put = %+v, want an ok row carrying a reading", got)
	}

	var rows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM activity_weather`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("activity_weather rows = %d, want 1 (upsert, not insert)", rows)
	}
}

func TestListPendingExcludesIndoorActivities(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	seedOutdoorRun(t, database, "outside", defaultActivityStart)
	mustInsertTrackpoint(t, database, "outside", 1, 39.7392, -104.9847)
	seedActivity(t, database, "treadmill", defaultActivityStart, "activity_run_details", "indoor")
	mustInsertTrackpoint(t, database, "treadmill", 1, 39.7392, -104.9847)

	pending, err := repo.ListPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ActivityID != "outside" {
		t.Fatalf("ListPending = %+v, want only the outdoor activity", pending)
	}
}

func TestListPendingExcludesSoftDeletedActivities(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	seedOutdoorRun(t, database, "gone", defaultActivityStart)
	mustInsertTrackpoint(t, database, "gone", 1, 39.7392, -104.9847)
	mustSoftDelete(t, database, "gone")

	pending, err := repo.ListPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("ListPending = %+v, want empty (soft-deleted activities are not work)", pending)
	}

	counts, err := repo.CountPending(context.Background())
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if counts != (PendingCounts{}) {
		t.Errorf("CountPending = %+v, want the zero split", counts)
	}
}

func TestListPendingExcludesActivitiesThatAlreadyHaveARow(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	seedOutdoorRun(t, database, "captured", defaultActivityStart)
	mustInsertTrackpoint(t, database, "captured", 1, 39.7392, -104.9847)
	seedOutdoorRun(t, database, "owed", defaultActivityStart.Add(-time.Hour))
	mustInsertTrackpoint(t, database, "owed", 1, 39.7392, -104.9847)

	// Every status is terminal for selection purposes: the presence of a row
	// is the checkpoint, which is what makes the backfill resumable.
	for _, status := range []ActivityStatus{ActivityStatusOK, ActivityStatusNoCoordinates, ActivityStatusUnavailable} {
		if err := repo.Put(ctx, ActivityWeather{
			ActivityID: "captured",
			Status:     status,
			FetchedAt:  defaultActivityStart,
		}); err != nil {
			t.Fatalf("Put %q: %v", status, err)
		}
		pending, err := repo.ListPending(ctx, 0)
		if err != nil {
			t.Fatalf("ListPending: %v", err)
		}
		if len(pending) != 1 || pending[0].ActivityID != "owed" {
			t.Fatalf("ListPending with a %q row = %+v, want only the uncaptured activity", status, pending)
		}
	}
}

func TestListPendingOrdersNewestFirstAndHonoursLimit(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	// Inserted oldest-first so a pass would need the ORDER BY, not luck.
	seedOutdoorRun(t, database, "old", base.Add(-48*time.Hour))
	seedOutdoorRun(t, database, "middle", base.Add(-24*time.Hour))
	seedOutdoorRun(t, database, "new", base)

	pending, err := repo.ListPending(ctx, 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	got := make([]string, len(pending))
	for i, p := range pending {
		got[i] = p.ActivityID
	}
	if len(got) != 3 || got[0] != "new" || got[1] != "middle" || got[2] != "old" {
		t.Fatalf("ListPending order = %v, want [new middle old]", got)
	}
	if !pending[0].StartTime.Equal(base) {
		t.Errorf("StartTime = %v, want %v", pending[0].StartTime, base)
	}

	limited, err := repo.ListPending(ctx, 2)
	if err != nil {
		t.Fatalf("ListPending limited: %v", err)
	}
	if len(limited) != 2 || limited[0].ActivityID != "new" || limited[1].ActivityID != "middle" {
		t.Fatalf("ListPending(limit 2) = %+v, want the two newest", limited)
	}
}

func TestListPendingReportsCoordinatesOfFirstPositionedTrackpoint(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)
	// Sequence 1 has no fix yet — the start coordinate is the first point
	// that actually carries one, not the first row.
	mustInsertTrackpoint(t, database, "a1", 1, nil, nil)
	// Sequences 2 and 3 are half-positioned, and they are what makes this a
	// test of ONE trackpoint rather than of two independent minima: lat and lon
	// are separate correlated subqueries, so dropping either one's check on the
	// other column pairs a latitude from one point with a longitude from
	// another and reports a coordinate no trackpoint ever recorded.
	mustInsertTrackpoint(t, database, "a1", 2, 1.0, nil)
	mustInsertTrackpoint(t, database, "a1", 3, nil, 2.0)
	mustInsertTrackpoint(t, database, "a1", 4, 39.7392, -104.9847)
	mustInsertTrackpoint(t, database, "a1", 5, 40.0, -105.0)

	pending, err := repo.ListPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("ListPending = %+v, want one activity", pending)
	}
	if pending[0].Lat == nil || pending[0].Lon == nil {
		t.Fatalf("coordinates = %v/%v, want the sequence-4 fix", pending[0].Lat, pending[0].Lon)
	}
	if *pending[0].Lat != 39.7392 || *pending[0].Lon != -104.9847 {
		t.Errorf("coordinates = %v/%v, want 39.7392/-104.9847", *pending[0].Lat, *pending[0].Lon)
	}
}

func TestListPendingReportsNilCoordinatesWithoutGPS(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	seedOutdoorRun(t, database, "no-track", defaultActivityStart)
	seedOutdoorRun(t, database, "unpositioned", defaultActivityStart.Add(-time.Hour))
	mustInsertTrackpoint(t, database, "unpositioned", 1, nil, nil)

	pending, err := repo.ListPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("ListPending = %+v, want both GPS-less activities", pending)
	}
	for _, p := range pending {
		if p.Lat != nil || p.Lon != nil {
			t.Errorf("%s coordinates = %v/%v, want nil/nil", p.ActivityID, p.Lat, p.Lon)
		}
	}
}

// The five-table union is the one place a new endurance type would be
// forgotten, so walk all five rather than trusting activity_run_details.
func TestListPendingCoversEveryEnduranceDetailTable(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	for i, table := range enduranceDetailTables {
		seedActivity(t, database, table, defaultActivityStart.Add(-time.Duration(i)*time.Hour), table, "outdoor")
		mustInsertTrackpoint(t, database, table, 1, 39.7392, -104.9847)
	}

	pending, err := repo.ListPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != len(enduranceDetailTables) {
		t.Fatalf("ListPending returned %d activities, want one per detail table (%d)", len(pending), len(enduranceDetailTables))
	}
}

func TestClearUnavailableLeavesTerminalNoCoordinatesAndOKAlone(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	for _, a := range []struct {
		id     string
		status ActivityStatus
	}{
		{"gone-1", ActivityStatusUnavailable},
		{"gone-2", ActivityStatusUnavailable},
		{"kept-nocoords", ActivityStatusNoCoordinates},
		{"kept-ok", ActivityStatusOK},
	} {
		seedOutdoorRun(t, database, a.id, defaultActivityStart)
		mustInsertTrackpoint(t, database, a.id, 1, 39.7392, -104.9847)
		row := ActivityWeather{ActivityID: a.id, Status: a.status, FetchedAt: defaultActivityStart}
		if a.status == ActivityStatusOK {
			row = okRow(a.id)
		}
		if err := repo.Put(ctx, row); err != nil {
			t.Fatalf("Put %s: %v", a.id, err)
		}
	}

	cleared, err := repo.ClearUnavailable(ctx)
	if err != nil {
		t.Fatalf("ClearUnavailable: %v", err)
	}
	if cleared != 2 {
		t.Errorf("ClearUnavailable = %d, want 2", cleared)
	}

	for _, id := range []string{"kept-nocoords", "kept-ok"} {
		row, getErr := repo.Get(ctx, id)
		if getErr != nil {
			t.Fatalf("Get %s: %v", id, getErr)
		}
		if row == nil {
			t.Errorf("%s was cleared; only 'unavailable' is retryable", id)
		}
	}

	// The cleared rows are eligible again — that is the whole point of the flag.
	pending, err := repo.ListPending(ctx, 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("ListPending after clear = %+v, want the two cleared activities", pending)
	}
}

// CountUnavailable is what --dry-run prints instead of clearing, so it has to
// describe exactly the set ClearUnavailable would delete — same statuses, same
// soft-delete scope — and it has to leave every row in place.
func TestCountUnavailableMatchesWhatClearWouldDelete(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()
	for _, a := range []struct {
		id      string
		status  ActivityStatus
		deleted bool
	}{
		{id: "gone-1", status: ActivityStatusUnavailable},
		{id: "gone-2", status: ActivityStatusUnavailable},
		{id: "dead", status: ActivityStatusUnavailable, deleted: true},
		{id: "kept-nocoords", status: ActivityStatusNoCoordinates},
		{id: "kept-ok", status: ActivityStatusOK},
	} {
		seedOutdoorRun(t, database, a.id, defaultActivityStart)
		mustInsertTrackpoint(t, database, a.id, 1, 39.7392, -104.9847)
		row := ActivityWeather{ActivityID: a.id, Status: a.status, FetchedAt: defaultActivityStart}
		if a.status == ActivityStatusOK {
			row = okRow(a.id)
		}
		if err := repo.Put(ctx, row); err != nil {
			t.Fatalf("Put %s: %v", a.id, err)
		}
		if a.deleted {
			mustSoftDelete(t, database, a.id)
		}
	}

	count, err := repo.CountUnavailable(ctx)
	if err != nil {
		t.Fatalf("CountUnavailable: %v", err)
	}
	if count != 2 {
		t.Errorf("CountUnavailable = %d, want 2 (the soft-deleted row can never become eligible again)", count)
	}

	// Counting is a read: every row, including the ones it counted, survives.
	var rows int
	if scanErr := database.QueryRow(`SELECT COUNT(*) FROM activity_weather`).Scan(&rows); scanErr != nil {
		t.Fatalf("count rows: %v", scanErr)
	}
	if rows != 5 {
		t.Fatalf("activity_weather has %d rows after CountUnavailable, want all 5", rows)
	}

	cleared, err := repo.ClearUnavailable(ctx)
	if err != nil {
		t.Fatalf("ClearUnavailable: %v", err)
	}
	if cleared != count {
		t.Errorf("ClearUnavailable deleted %d rows, want the %d CountUnavailable promised", cleared, count)
	}
}

func TestCountOKAndCountPendingAgreeWithTheFixtures(t *testing.T) {
	repo, database := newActivityWeatherRepo(t)
	ctx := context.Background()

	// Two captured 'ok', one captured 'unavailable', two pending with GPS,
	// one pending without, one indoor, one soft-deleted.
	for _, id := range []string{"ok-1", "ok-2"} {
		seedOutdoorRun(t, database, id, defaultActivityStart)
		mustInsertTrackpoint(t, database, id, 1, 39.7392, -104.9847)
		if err := repo.Put(ctx, okRow(id)); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	seedOutdoorRun(t, database, "terminal", defaultActivityStart)
	if err := repo.Put(ctx, ActivityWeather{
		ActivityID: "terminal", Status: ActivityStatusUnavailable, FetchedAt: defaultActivityStart,
	}); err != nil {
		t.Fatalf("Put terminal: %v", err)
	}
	for _, id := range []string{"pending-1", "pending-2"} {
		seedOutdoorRun(t, database, id, defaultActivityStart)
		mustInsertTrackpoint(t, database, id, 1, 39.7392, -104.9847)
	}
	seedOutdoorRun(t, database, "pending-nogps", defaultActivityStart)
	seedActivity(t, database, "indoor", defaultActivityStart, "activity_run_details", "indoor")
	seedOutdoorRun(t, database, "deleted", defaultActivityStart)
	mustInsertTrackpoint(t, database, "deleted", 1, 39.7392, -104.9847)
	mustSoftDelete(t, database, "deleted")

	okCount, err := repo.CountOK(ctx)
	if err != nil {
		t.Fatalf("CountOK: %v", err)
	}
	if okCount != 2 {
		t.Errorf("CountOK = %d, want 2", okCount)
	}

	counts, err := repo.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	want := PendingCounts{Eligible: 2, NoCoordinates: 1}
	if counts != want {
		t.Errorf("CountPending = %+v, want %+v", counts, want)
	}

	// The split must describe the same set ListPending will walk, or
	// --dry-run quotes a number the run then contradicts.
	pending, err := repo.ListPending(ctx, 0)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != counts.Eligible+counts.NoCoordinates {
		t.Errorf("ListPending returned %d, want %d from the counts", len(pending), counts.Eligible+counts.NoCoordinates)
	}

	// A soft-deleted activity's retained 'ok' row must not keep counting.
	if putErr := repo.Put(ctx, okRow("deleted")); putErr != nil {
		t.Fatalf("Put deleted: %v", putErr)
	}
	okCount, err = repo.CountOK(ctx)
	if err != nil {
		t.Fatalf("CountOK after soft delete: %v", err)
	}
	if okCount != 2 {
		t.Errorf("CountOK = %d with a soft-deleted row present, want 2", okCount)
	}
}
