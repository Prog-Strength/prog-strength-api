package activity

import (
	"encoding/json"
	"math"
)

// Route simplification tuning. See sows/sow-trail-map.md → Algorithms.
const (
	// routeGapMeters ends a segment when two consecutive positioned points
	// are farther apart than this (teleport / GPS acquisition glitch).
	routeGapMeters = 50.0
	// routeSimplifyEpsilon is the Douglas–Peucker tolerance, in DEGREES, on
	// (lat, lon) treated as a local planar approximation. ~5.6 m N–S; drops
	// ~94% of points on a real trail file at 1 Hz.
	routeSimplifyEpsilon = 5e-5
	// routeCoordScale truncates coordinates to 6 decimal places (~10 cm) on
	// write. Raw float64 fidelity survives in the archived S3 TCX.
	routeCoordScale = 1e6
)

// latLon is a positioned point used by the simplifier. Coordinates are full
// float64 precision until serialization truncates them.
type latLon struct {
	Lat float64
	Lon float64
}

// geoFeature is the serialized route: a GeoJSON Feature wrapping a
// MultiLineString whose coordinates are [longitude, latitude] pairs
// (RFC 7946), with the pre-computed bounding box in properties.
type geoFeature struct {
	Type       string        `json:"type"`
	Geometry   geoGeometry   `json:"geometry"`
	Properties geoProperties `json:"properties"`
}

type geoGeometry struct {
	Type        string         `json:"type"`
	Coordinates [][][2]float64 `json:"coordinates"`
}

type geoProperties struct {
	Bounds geoBounds `json:"bounds"`
}

type geoBounds struct {
	MinLat float64 `json:"min_lat"`
	MinLng float64 `json:"min_lng"`
	MaxLat float64 `json:"max_lat"`
	MaxLng float64 `json:"max_lng"`
}

// buildRoute derives the simplified GeoJSON route from the FULL raw
// positioned trackpoint series (before the ~300-point chart downsample). It
// gap-splits into segments (never bridging a NULL Position or a >50 m
// teleport), runs Douglas–Peucker on each, truncates coordinates to 6
// decimals, and serializes a MultiLineString Feature with bounds. Returns
// nil when fewer than two positioned points remain after splitting (no
// renderable route — the caller stores NULL and the map is omitted).
func buildRoute(tps []parsedTrackpoint) *string {
	segments := splitSegments(tps)

	coords := make([][][2]float64, 0, len(segments))
	first := true
	var b geoBounds
	for _, seg := range segments {
		simplified := rdp(seg, routeSimplifyEpsilon)
		if len(simplified) < 2 {
			continue
		}
		line := make([][2]float64, 0, len(simplified))
		for _, p := range simplified {
			lat := truncateCoord(p.Lat)
			lon := truncateCoord(p.Lon)
			line = append(line, [2]float64{lon, lat})
			if first {
				b = geoBounds{MinLat: lat, MinLng: lon, MaxLat: lat, MaxLng: lon}
				first = false
				continue
			}
			b.MinLat = math.Min(b.MinLat, lat)
			b.MaxLat = math.Max(b.MaxLat, lat)
			b.MinLng = math.Min(b.MinLng, lon)
			b.MaxLng = math.Max(b.MaxLng, lon)
		}
		coords = append(coords, line)
	}

	if len(coords) == 0 {
		return nil
	}

	f := geoFeature{
		Type: "Feature",
		Geometry: geoGeometry{
			Type:        "MultiLineString",
			Coordinates: coords,
		},
		Properties: geoProperties{Bounds: b},
	}
	raw, err := json.Marshal(f)
	if err != nil {
		// The struct is fixed-shape and all-finite; marshaling cannot fail in
		// practice. Treat a failure as "no route" rather than panicking ingest.
		return nil
	}
	s := string(raw)
	return &s
}

// splitSegments walks the series and cuts a new segment whenever a NULL
// Position appears (the null point is excluded) or two consecutive
// positioned points are more than routeGapMeters apart. Segments of any
// length are returned; the caller drops those that simplify below 2 points.
func splitSegments(tps []parsedTrackpoint) [][]latLon {
	var segments [][]latLon
	var cur []latLon
	var prev *latLon

	flush := func() {
		if len(cur) > 0 {
			segments = append(segments, cur)
			cur = nil
		}
		prev = nil
	}

	for _, tp := range tps {
		if tp.Latitude == nil || tp.Longitude == nil {
			flush()
			continue
		}
		p := latLon{Lat: *tp.Latitude, Lon: *tp.Longitude}
		if prev != nil && haversineMeters(prev.Lat, prev.Lon, p.Lat, p.Lon) > routeGapMeters {
			flush()
		}
		cur = append(cur, p)
		last := p
		prev = &last
	}
	flush()
	return segments
}

// haversineMeters is the great-circle distance between two WGS84 points.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusM = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// rdp is Douglas–Peucker line simplification working in degrees on the
// (lat, lon) plane. epsilon is the max perpendicular deviation a point may
// have from the segment chord before it must be kept. Endpoints are always
// retained, so a run of >=2 input points yields >=2 output points.
func rdp(points []latLon, epsilon float64) []latLon {
	if len(points) < 3 {
		return points
	}
	first, last := points[0], points[len(points)-1]
	maxDist := 0.0
	idx := 0
	for i := 1; i < len(points)-1; i++ {
		d := perpendicularDistance(points[i], first, last)
		if d > maxDist {
			maxDist = d
			idx = i
		}
	}
	if maxDist <= epsilon {
		return []latLon{first, last}
	}
	left := rdp(points[:idx+1], epsilon)
	right := rdp(points[idx:], epsilon)
	// Drop the duplicated join point (idx appears in both halves).
	return append(left[:len(left)-1], right...)
}

// perpendicularDistance is the distance from p to the line through a and b,
// treating lat/lon as planar (x=lon, y=lat). When a == b it degenerates to
// the point distance.
func perpendicularDistance(p, a, b latLon) float64 {
	dx := b.Lon - a.Lon
	dy := b.Lat - a.Lat
	if dx == 0 && dy == 0 {
		return math.Hypot(p.Lon-a.Lon, p.Lat-a.Lat)
	}
	num := math.Abs(dy*(p.Lon-a.Lon) - dx*(p.Lat-a.Lat))
	return num / math.Hypot(dx, dy)
}

// truncateCoord truncates a coordinate toward zero to 6 decimal places
// (~10 cm). Truncation, not rounding, matches the DB write rule.
func truncateCoord(v float64) float64 {
	return math.Trunc(v*routeCoordScale) / routeCoordScale
}
