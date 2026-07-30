package activity

import (
	"encoding/json"
	"testing"
)

// The wire contract the linked map/profile depends on: every detail read
// carries latitude, longitude, and grade_percent per trackpoint, always as
// present keys (rendered null when absent) per the DTO convention in
// handler.go. Answers sows/sow-trail-map.md Open Question 2.

func hikeWithTrackpoints(tps []Trackpoint) Activity {
	return Activity{
		ActivityType:    ActivityHiking,
		DistanceMeters:  100,
		DurationSeconds: 600,
		Trackpoints:     tps,
	}
}

// positionedClimb: 10 m steps rising 1 m each, with coordinates — a 10% grade
// from index 3 on.
func positionedClimb(n int) []Trackpoint {
	tps := make([]Trackpoint, 0, n+1)
	for i := 0; i <= n; i++ {
		tps = append(tps, Trackpoint{
			Sequence:        i,
			ElapsedSeconds:  i * 10,
			DistanceMeters:  float64(i) * 10,
			ElevationMeters: fp(3300 + float64(i)),
			Latitude:        fp(39.39 + float64(i)*0.0001),
			Longitude:       fp(-106.06 - float64(i)*0.0001),
		})
	}
	return tps
}

func TestTrackpointDTO_CarriesPositionAndGrade(t *testing.T) {
	dto := toActivityDTO(hikeWithTrackpoints(positionedClimb(10)), true)

	if len(dto.Trackpoints) != 11 {
		t.Fatalf("len(trackpoints) = %d, want 11", len(dto.Trackpoints))
	}
	got := dto.Trackpoints[5]
	if got.Latitude == nil || got.Longitude == nil {
		t.Fatalf("index 5: lat/lng = %v/%v, want coordinates", got.Latitude, got.Longitude)
	}
	if *got.Latitude != 39.3905 {
		t.Errorf("latitude = %v, want 39.3905", *got.Latitude)
	}
	if got.GradePercent == nil {
		t.Fatal("index 5: grade_percent = nil, want 10")
	}
	if *got.GradePercent != 10 {
		t.Errorf("grade_percent = %v, want 10", *got.GradePercent)
	}
}

// Index alignment is the contract that makes ONE scrub index drive both the
// chart and the map. If the projection ever reorders or filters, this breaks.
func TestTrackpointDTO_IsIndexAlignedWithStoredTrackpoints(t *testing.T) {
	tps := positionedClimb(20)
	dto := toActivityDTO(hikeWithTrackpoints(tps), true)

	if len(dto.Trackpoints) != len(tps) {
		t.Fatalf("len = %d, want %d", len(dto.Trackpoints), len(tps))
	}
	for i := range tps {
		if dto.Trackpoints[i].Sequence != tps[i].Sequence {
			t.Fatalf("index %d: sequence = %d, want %d",
				i, dto.Trackpoints[i].Sequence, tps[i].Sequence)
		}
		if dto.Trackpoints[i].DistanceMeters != tps[i].DistanceMeters {
			t.Fatalf("index %d: distance drifted", i)
		}
	}
}

// An indoor / no-GPS activity: the three keys must still SHIP, as nulls. The
// client branches on presence, so a missing key is a different bug from a null
// one.
func TestTrackpointDTO_KeysArePresentAndNullWithoutPositionOrElevation(t *testing.T) {
	tps := []Trackpoint{
		{Sequence: 0, ElapsedSeconds: 0, DistanceMeters: 0},
		{Sequence: 1, ElapsedSeconds: 10, DistanceMeters: 40},
		{Sequence: 2, ElapsedSeconds: 20, DistanceMeters: 80},
	}
	dto := toActivityDTO(hikeWithTrackpoints(tps), true)

	for i, tp := range dto.Trackpoints {
		if tp.Latitude != nil || tp.Longitude != nil || tp.GradePercent != nil {
			t.Fatalf("index %d: want all three nil, got %v/%v/%v",
				i, tp.Latitude, tp.Longitude, tp.GradePercent)
		}
	}

	raw, err := json.Marshal(dto.Trackpoints[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"latitude", "longitude", "grade_percent"} {
		v, ok := keyed[key]
		if !ok {
			t.Errorf("key %q missing from the wire shape (no omitempty allowed)", key)
			continue
		}
		if string(v) != "null" {
			t.Errorf("key %q = %s, want null", key, v)
		}
	}
}

// List responses never load the per-point stream, so the added fields cost
// nothing there.
func TestTrackpointDTO_OmittedEntirelyWithoutTrackpoints(t *testing.T) {
	dto := toActivityDTO(hikeWithTrackpoints(positionedClimb(10)), false)
	if dto.Trackpoints != nil {
		t.Fatalf("trackpoints = %v, want nil on a list projection", dto.Trackpoints)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := keyed["trackpoints"]; ok {
		t.Error("trackpoints key present on a list projection; omitempty should drop it")
	}
}
