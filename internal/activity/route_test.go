package activity

import (
	"encoding/json"
	"math"
	"testing"
)

func ll(lat, lon float64) parsedTrackpoint {
	la, lo := lat, lon
	return parsedTrackpoint{Latitude: &la, Longitude: &lo}
}

func noPos() parsedTrackpoint { return parsedTrackpoint{} }

func decodeRoute(t *testing.T, s *string) geoFeature {
	t.Helper()
	if s == nil {
		t.Fatal("expected a route, got nil")
	}
	var f geoFeature
	if err := json.Unmarshal([]byte(*s), &f); err != nil {
		t.Fatalf("route is not valid json: %v", err)
	}
	return f
}

func TestBuildRoute_NilWhenNoPositions(t *testing.T) {
	tps := []parsedTrackpoint{noPos(), noPos()}
	if got := buildRoute(tps); got != nil {
		t.Fatalf("expected nil route, got %q", *got)
	}
}

func TestBuildRoute_NilWhenSinglePoint(t *testing.T) {
	tps := []parsedTrackpoint{ll(40.0, -105.0)}
	if got := buildRoute(tps); got != nil {
		t.Fatalf("expected nil route for a single positioned point, got %q", *got)
	}
}

func TestBuildRoute_SingleSegmentIsMultiLineString(t *testing.T) {
	tps := []parsedTrackpoint{
		ll(40.000000, -105.000000),
		ll(40.000100, -105.000000),
		ll(40.000200, -105.000000),
		ll(40.000300, -105.000000),
	}
	f := decodeRoute(t, buildRoute(tps))
	if f.Type != "Feature" {
		t.Fatalf("type = %q, want Feature", f.Type)
	}
	if f.Geometry.Type != "MultiLineString" {
		t.Fatalf("geometry type = %q, want MultiLineString", f.Geometry.Type)
	}
	if len(f.Geometry.Coordinates) != 1 {
		t.Fatalf("segments = %d, want 1", len(f.Geometry.Coordinates))
	}
	first := f.Geometry.Coordinates[0][0]
	if math.Abs(first[0]-(-105.0)) > 1e-9 || math.Abs(first[1]-40.0) > 1e-9 {
		t.Fatalf("first coord = %v, want [-105, 40]", first)
	}
}

func TestBuildRoute_SplitsOnNullPosition(t *testing.T) {
	tps := []parsedTrackpoint{
		ll(40.000000, -105.000000),
		ll(40.000100, -105.000000),
		noPos(),
		ll(40.001000, -105.000000),
		ll(40.001100, -105.000000),
	}
	f := decodeRoute(t, buildRoute(tps))
	if len(f.Geometry.Coordinates) != 2 {
		t.Fatalf("segments = %d, want 2 (gap split on null position)", len(f.Geometry.Coordinates))
	}
}

func TestBuildRoute_SplitsOnTeleport(t *testing.T) {
	tps := []parsedTrackpoint{
		ll(40.000000, -105.000000),
		ll(40.000200, -105.000000),
		ll(40.100000, -105.000000),
		ll(40.100200, -105.000000),
	}
	f := decodeRoute(t, buildRoute(tps))
	if len(f.Geometry.Coordinates) != 2 {
		t.Fatalf("segments = %d, want 2 (teleport split)", len(f.Geometry.Coordinates))
	}
}

func TestBuildRoute_DropsShortSegments(t *testing.T) {
	tps := []parsedTrackpoint{
		ll(40.000000, -105.000000),
		ll(40.000100, -105.000000),
		noPos(),
		ll(41.000000, -105.000000),
		noPos(),
	}
	f := decodeRoute(t, buildRoute(tps))
	if len(f.Geometry.Coordinates) != 1 {
		t.Fatalf("segments = %d, want 1 (isolated single point dropped)", len(f.Geometry.Coordinates))
	}
}

func TestBuildRoute_SimplifiesAndComputesBounds(t *testing.T) {
	var tps []parsedTrackpoint
	for i := 0; i < 500; i++ {
		tps = append(tps, ll(40.0+float64(i)*0.00001, -105.0))
	}
	f := decodeRoute(t, buildRoute(tps))
	pts := f.Geometry.Coordinates[0]
	if len(pts) > 10 {
		t.Fatalf("simplified line kept %d points, expected a large reduction", len(pts))
	}
	b := f.Properties.Bounds
	if b.MinLat > b.MaxLat || b.MinLng > b.MaxLng {
		t.Fatalf("bounds inverted: %+v", b)
	}
	if math.Abs(b.MinLat-40.0) > 1e-6 {
		t.Fatalf("min_lat = %v, want ~40.0", b.MinLat)
	}
}

func TestBuildRoute_TruncatesToSixDecimals(t *testing.T) {
	tps := []parsedTrackpoint{
		ll(40.1234567, -105.7654321),
		ll(40.1235000, -105.7655000),
	}
	f := decodeRoute(t, buildRoute(tps))
	c := f.Geometry.Coordinates[0][0]
	if math.Abs(c[1]-40.123456) > 1e-9 {
		t.Fatalf("lat = %v, want 40.123456", c[1])
	}
	if math.Abs(c[0]-(-105.765432)) > 1e-9 {
		t.Fatalf("lng = %v, want -105.765432", c[0])
	}
}

func TestBuildRoute_RealTrailFixtureReduces(t *testing.T) {
	p, err := parseTCX(readFixture(t, "typical_5k.tcx"))
	if err != nil {
		t.Fatalf("parseTCX: %v", err)
	}
	var raw int
	for _, tp := range p.Trackpoints {
		if tp.Latitude != nil {
			raw++
		}
	}
	f := decodeRoute(t, buildRoute(p.Trackpoints))
	var simplified int
	for _, seg := range f.Geometry.Coordinates {
		simplified += len(seg)
	}
	if simplified == 0 || simplified >= raw {
		t.Fatalf("simplified=%d raw=%d, expected a material reduction", simplified, raw)
	}
}
