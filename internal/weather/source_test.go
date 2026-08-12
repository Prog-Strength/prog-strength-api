package weather

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Source
		ok   bool
	}{
		{"absent defaults to tile", "", SourceTile, true},
		{"explicit tile", "tile", SourceTile, true},
		{"agent", "agent", SourceAgent, true},
		{"unknown value rejected", "mobile", "", false},
		{"case sensitive", "Agent", "", false},
		{"whitespace is not trimmed away into a default", " ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseSource(tt.raw)
			if ok != tt.ok {
				t.Fatalf("ParseSource(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ParseSource(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// servedBySource reads the served counter for one surface. Every other test in
// the package drives the tile, so a leg that asserts the agent series moved AND
// the tile series did not is what actually pins the label to its argument.
func servedBySource(source Source) float64 {
	return testutil.ToFloat64(requestsTotal.WithLabelValues("served", string(source)))
}

// The label is what makes the shared budget attributable, so assert it lands
// on the counter rather than trusting the call sites.
func TestReadingsLabelsTheRequestingSource(t *testing.T) {
	agentBefore, tileBefore := servedBySource(SourceAgent), servedBySource(SourceTile)

	// Cold cache plus a working fake provider is the "served" disposition.
	svc, _, _, _ := newTestService(t, svcCfg(), newFakeProvider())
	if r := svc.Readings(context.Background(), testLat, testLon, SourceAgent); r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
	}

	if got := servedBySource(SourceAgent) - agentBefore; got != 1 {
		t.Fatalf("agent-sourced served count delta = %v, want 1", got)
	}
	if got := servedBySource(SourceTile) - tileBefore; got != 0 {
		t.Fatalf("tile served count moved by %v on an agent request, want 0", got)
	}
}

// Search and Reverse reach the counter through geocode rather than Readings,
// so that half of the threading needs its own leg: without one, geocode could
// hardcode the tile and every test would still pass.
func TestSearchLabelsTheRequestingSource(t *testing.T) {
	agentBefore, tileBefore := servedBySource(SourceAgent), servedBySource(SourceTile)

	svc, _, _, _ := newTestService(t, svcCfg(), newFakeProvider())
	_, status, err := svc.Search(context.Background(), "denver", 5, SourceAgent)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want %q", status, StatusOK)
	}

	if got := servedBySource(SourceAgent) - agentBefore; got != 1 {
		t.Fatalf("agent-sourced served count delta = %v, want 1", got)
	}
	if got := servedBySource(SourceTile) - tileBefore; got != 0 {
		t.Fatalf("tile served count moved by %v on an agent search, want 0", got)
	}
}
