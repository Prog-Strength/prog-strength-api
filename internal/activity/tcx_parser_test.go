package activity

import (
	"os"
	"path/filepath"
	"testing"
)

// readFixture loads a committed .tcx fixture from testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseTCX_Typical5k(t *testing.T) {
	p, err := parseTCX(readFixture(t, "typical_5k.tcx"))
	if err != nil {
		t.Fatalf("parseTCX returned error: %v", err)
	}

	// Namespace-agnostic matching: the fixture declares the Garmin default
	// namespace, so a non-empty Sport/ActivityID/trackpoints proves
	// encoding/xml bound the elements by local name.
	if p.Sport != "Running" {
		t.Errorf("Sport = %q, want Running", p.Sport)
	}
	if p.ActivityID != "2026-01-02T08:00:00Z" {
		t.Errorf("ActivityID = %q, want the Id element value", p.ActivityID)
	}
	if got := len(p.Trackpoints); got != 600 {
		t.Errorf("trackpoint count = %d, want 600", got)
	}
	if p.Notes == nil || *p.Notes != "Morning Run" {
		t.Errorf("Notes = %v, want Morning Run", p.Notes)
	}
	if !p.hasCalories || len(p.LapCalories) != 1 || p.LapCalories[0] != 350 {
		t.Errorf("lap calories = %v (hasCalories=%v), want [350]", p.LapCalories, p.hasCalories)
	}

	// First trackpoint carries HR and altitude; spot-check the flattening.
	first := p.Trackpoints[0]
	if first.HeartRateBpm == nil {
		t.Fatal("first trackpoint HR is nil, want a value")
	}
	if first.AltitudeMeters == nil {
		t.Fatal("first trackpoint altitude is nil, want a value")
	}
}

func TestParseTCX_Malformed(t *testing.T) {
	if _, err := parseTCX(readFixture(t, "malformed.tcx")); err == nil {
		t.Fatal("parseTCX(malformed) returned nil error, want a parse error")
	}
}

// TestParseTCX_HasPosition covers the position-presence detection that drives
// indoor/outdoor defaulting at ingest: a running track whose trackpoints carry
// <Position> parses with HasPosition true, one without parses false. We only
// assert the boolean — no lat/lon is stored.
func TestParseTCX_HasPosition(t *testing.T) {
	withPos := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">
  <Activities>
    <Activity Sport="Running">
      <Id>with-position-001</Id>
      <Lap StartTime="2026-01-02T08:00:00Z">
        <TotalTimeSeconds>2</TotalTimeSeconds>
        <DistanceMeters>20.00</DistanceMeters>
        <Track>
          <Trackpoint>
            <Time>2026-01-02T08:00:00Z</Time>
            <DistanceMeters>0.00</DistanceMeters>
            <Position><LatitudeDegrees>40.0</LatitudeDegrees><LongitudeDegrees>-105.0</LongitudeDegrees></Position>
          </Trackpoint>
          <Trackpoint>
            <Time>2026-01-02T08:00:02Z</Time>
            <DistanceMeters>20.00</DistanceMeters>
            <Position><LatitudeDegrees>40.0001</LatitudeDegrees><LongitudeDegrees>-105.0001</LongitudeDegrees></Position>
          </Trackpoint>
        </Track>
      </Lap>
    </Activity>
  </Activities>
</TrainingCenterDatabase>`)

	noPos := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">
  <Activities>
    <Activity Sport="Running">
      <Id>no-position-001</Id>
      <Lap StartTime="2026-01-02T08:00:00Z">
        <TotalTimeSeconds>2</TotalTimeSeconds>
        <DistanceMeters>20.00</DistanceMeters>
        <Track>
          <Trackpoint>
            <Time>2026-01-02T08:00:00Z</Time>
            <DistanceMeters>0.00</DistanceMeters>
          </Trackpoint>
          <Trackpoint>
            <Time>2026-01-02T08:00:02Z</Time>
            <DistanceMeters>20.00</DistanceMeters>
          </Trackpoint>
        </Track>
      </Lap>
    </Activity>
  </Activities>
</TrainingCenterDatabase>`)

	pWith, err := parseTCX(withPos)
	if err != nil {
		t.Fatalf("parseTCX(withPos) error: %v", err)
	}
	if !pWith.HasPosition {
		t.Error("HasPosition = false for a track carrying <Position>, want true")
	}

	pNo, err := parseTCX(noPos)
	if err != nil {
		t.Fatalf("parseTCX(noPos) error: %v", err)
	}
	if pNo.HasPosition {
		t.Error("HasPosition = true for a track with no <Position>, want false")
	}
}

func TestParseTCX_CapturesPositionCoords(t *testing.T) {
	p, err := parseTCX(readFixture(t, "typical_5k.tcx"))
	if err != nil {
		t.Fatalf("parseTCX: %v", err)
	}
	if !p.HasPosition {
		t.Fatal("expected HasPosition true for GPS fixture")
	}
	var positioned int
	for _, tp := range p.Trackpoints {
		if tp.Latitude != nil && tp.Longitude != nil {
			positioned++
			if *tp.Latitude < -90 || *tp.Latitude > 90 {
				t.Fatalf("latitude out of range: %v", *tp.Latitude)
			}
			if *tp.Longitude < -180 || *tp.Longitude > 180 {
				t.Fatalf("longitude out of range: %v", *tp.Longitude)
			}
		}
	}
	if positioned == 0 {
		t.Fatal("expected at least one trackpoint with captured lat/lon")
	}
}

func TestParseTCX_TreadmillHasNoCoords(t *testing.T) {
	p, err := parseTCX(readFixture(t, "treadmill_5k.tcx"))
	if err != nil {
		t.Fatalf("parseTCX: %v", err)
	}
	for _, tp := range p.Trackpoints {
		if tp.Latitude != nil || tp.Longitude != nil {
			t.Fatal("treadmill fixture should have no coordinates")
		}
	}
}
