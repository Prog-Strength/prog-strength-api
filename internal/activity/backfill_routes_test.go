package activity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFromFixture creates a running activity whose archived TCX is the given
// fixture bytes and whose trackpoint rows match a fresh summarize of those
// bytes — i.e. exactly the state the live ingest path produces. It returns the
// created activity plus the fixture bytes so a test can re-parse them and
// assert shared-helper parity with the backfill.
func seedFromFixture(t *testing.T, repo *SQLiteRepository, userID, sourceID, fixture string) (*Activity, []byte) {
	t.Helper()
	ctx := context.Background()

	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	parsed, err := parseTCX(raw)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", fixture, err)
	}
	if err := validate(parsed); err != nil {
		t.Fatalf("validate fixture %s: %v", fixture, err)
	}

	a := summarize(parsed, ActivityRunning)
	a.UserID = userID
	a.IngestSource = IngestManualTCX
	a.SourceActivityID = sourceID
	if a.StartTime.IsZero() {
		a.StartTime = mustTime(t, "2026-06-01T07:00:00Z")
	}
	if err := repo.Create(ctx, &a, raw); err != nil {
		t.Fatalf("Create from fixture %s: %v", fixture, err)
	}
	return &a, raw
}

// routeGeoJSON reads the raw route_geojson column for an activity, returning
// (value, isNull).
func routeGeoJSON(t *testing.T, repo *SQLiteRepository, id string) (string, bool) {
	t.Helper()
	var route *string
	if err := repo.db.QueryRow(`SELECT route_geojson FROM activities WHERE id = ?`, id).Scan(&route); err != nil {
		t.Fatalf("read route_geojson: %v", err)
	}
	if route == nil {
		return "", true
	}
	return *route, false
}

// nullOutRoute simulates the pre-feature state: route absent and every
// trackpoint's coordinates cleared, so the backfill has real work to do.
func nullOutRoute(t *testing.T, repo *SQLiteRepository, id string) {
	t.Helper()
	if _, err := repo.db.Exec(`UPDATE activities SET route_geojson = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("null route: %v", err)
	}
	if _, err := repo.db.Exec(`UPDATE activity_trackpoints SET latitude = NULL, longitude = NULL WHERE activity_id = ?`, id); err != nil {
		t.Fatalf("null coords: %v", err)
	}
}

func TestBackfillActivityRoutes_OutdoorWithPosition(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a, raw := seedFromFixture(t, repo, "u1", "outdoor-1", "typical_5k.tcx")
	nullOutRoute(t, repo, a.ID)

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes: %v", err)
	}

	route, isNull := routeGeoJSON(t, repo, a.ID)
	if isNull {
		t.Fatal("route_geojson is NULL, want a MultiLineString feature")
	}
	if !strings.Contains(route, "MultiLineString") {
		t.Fatalf("route_geojson missing MultiLineString: %s", route)
	}

	// Shared-helper parity: the backfilled route and per-sequence lat/lon must
	// equal a fresh summarize of the same bytes, or the two paths have drifted.
	parsed, err := parseTCX(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	want := summarize(parsed, ActivityRunning)
	if want.RouteGeoJSON == nil {
		t.Fatal("fixture unexpectedly produced no route")
	}
	if route != *want.RouteGeoJSON {
		t.Fatalf("route drift:\n got=%s\nwant=%s", route, *want.RouteGeoJSON)
	}

	got, err := repo.loadTrackpoints(ctx, a.ID)
	if err != nil {
		t.Fatalf("loadTrackpoints: %v", err)
	}
	if len(got) != len(want.Trackpoints) {
		t.Fatalf("trackpoint count = %d, want %d", len(got), len(want.Trackpoints))
	}
	positioned := 0
	for i := range want.Trackpoints {
		w := want.Trackpoints[i]
		g := got[i]
		if g.Sequence != w.Sequence {
			t.Fatalf("sequence mismatch at %d: got %d want %d", i, g.Sequence, w.Sequence)
		}
		switch {
		case w.Latitude == nil:
			if g.Latitude != nil {
				t.Fatalf("seq %d: latitude = %v, want NULL", w.Sequence, *g.Latitude)
			}
		case g.Latitude == nil:
			t.Fatalf("seq %d: latitude NULL, want %v", w.Sequence, *w.Latitude)
		default:
			positioned++
			if *g.Latitude != *w.Latitude || *g.Longitude != *w.Longitude {
				t.Fatalf("seq %d coords: got (%v,%v) want (%v,%v)",
					w.Sequence, *g.Latitude, *g.Longitude, *w.Latitude, *w.Longitude)
			}
		}
	}
	if positioned == 0 {
		t.Fatal("expected some kept trackpoints to have coordinates")
	}
}

func TestBackfillActivityRoutes_NoPositionOutdoorStaysNull(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	// treadmill_5k.tcx has no <Position>, so summarize tags it indoor; force
	// the row outdoor to exercise the "selected but no geometry" branch.
	a, _ := seedFromFixture(t, repo, "u1", "no-pos-1", "treadmill_5k.tcx")
	if _, err := repo.db.Exec(`UPDATE activities SET environment = 'outdoor', route_geojson = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("force outdoor: %v", err)
	}

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes: %v", err)
	}

	if _, isNull := routeGeoJSON(t, repo, a.ID); !isNull {
		t.Fatal("route_geojson should stay NULL for a no-Position activity")
	}
}

func TestBackfillActivityRoutes_Idempotent(t *testing.T) {
	t.Parallel()
	repo, arch := newRepo(t)
	ctx := context.Background()

	// Seeded normally: Create already wrote the route, so the row is not in
	// the selection set and must be left byte-identical.
	a, _ := seedFromFixture(t, repo, "u1", "idem-1", "typical_5k.tcx")
	before, isNull := routeGeoJSON(t, repo, a.ID)
	if isNull {
		t.Fatal("expected a route from Create")
	}

	// Poison the archived object so a re-fetch would produce a different
	// value; proving the row is untouched proves it was never re-processed.
	if err := arch.Put(ctx, a.TCXS3Key, []byte("<TrainingCenterDatabase/>"), ObjectMetadata{IngestSource: IngestManualTCX}); err != nil {
		t.Fatalf("poison archive: %v", err)
	}

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes: %v", err)
	}

	after, isNull := routeGeoJSON(t, repo, a.ID)
	if isNull {
		t.Fatal("route_geojson became NULL after idempotent backfill")
	}
	if after != before {
		t.Fatalf("route changed on idempotent run:\n got=%s\nwant=%s", after, before)
	}
}

func TestBackfillActivityRoutes_MissingS3Object(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a, _ := seedFromFixture(t, repo, "u1", "missing-1", "typical_5k.tcx")
	if _, err := repo.db.Exec(`UPDATE activities SET tcx_s3_key = 'bogus/key.tcx', route_geojson = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("bogus key: %v", err)
	}

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes should skip, not fail: %v", err)
	}
	if _, isNull := routeGeoJSON(t, repo, a.ID); !isNull {
		t.Fatal("route_geojson should stay NULL when the S3 object is missing")
	}
}

func TestBackfillActivityRoutes_OutdoorStrengthNeverSelected(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	// A strength_training row defaults to environment='outdoor' with a NULL
	// route. Even when its archived TCX carries GPS (a misattached running
	// file), the backfill must skip it: strength ingest never builds a route,
	// and re-fetching every strength TCX on every boot is the waste the
	// selection filter is meant to avoid. Seed from a GPS-bearing fixture so a
	// missing filter would visibly write a route.
	a, _ := seedFromFixture(t, repo, "u1", "strength-1", "typical_5k.tcx")
	if _, err := repo.db.Exec(`UPDATE activities SET activity_type = ?, environment = 'outdoor', route_geojson = NULL WHERE id = ?`, ActivityStrengthTraining, a.ID); err != nil {
		t.Fatalf("force strength: %v", err)
	}
	nullOutRoute(t, repo, a.ID)

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes: %v", err)
	}
	if _, isNull := routeGeoJSON(t, repo, a.ID); !isNull {
		t.Fatal("strength route_geojson should be untouched (still NULL)")
	}
}

func TestBackfillActivityRoutes_IndoorNeverSelected(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	// An indoor row with a NULL route must never be selected — otherwise every
	// treadmill TCX gets re-fetched on every boot.
	a := newActivity("u1", IngestManualTCX, "indoor-1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	a.Environment = EnvironmentIndoor
	if err := repo.Create(ctx, a, []byte("<TrainingCenterDatabase/>")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.db.Exec(`UPDATE activities SET route_geojson = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("null route: %v", err)
	}

	if err := repo.BackfillActivityRoutes(ctx); err != nil {
		t.Fatalf("BackfillActivityRoutes: %v", err)
	}
	if _, isNull := routeGeoJSON(t, repo, a.ID); !isNull {
		t.Fatal("indoor route_geojson should be untouched (still NULL)")
	}
}
