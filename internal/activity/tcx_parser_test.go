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
