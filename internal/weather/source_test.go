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

// The label is what makes the shared budget attributable, so assert it lands
// on the counter rather than trusting the call sites.
func TestReadings_LabelsTheRequestingSource(t *testing.T) {
	served := func(source Source) float64 {
		return testutil.ToFloat64(requestsTotal.WithLabelValues("served", string(source)))
	}
	agentBefore, tileBefore := served(SourceAgent), served(SourceTile)

	// Cold cache plus a working fake provider is the "served" disposition.
	svc, _, _, _ := newTestService(t, svcCfg(), newFakeProvider())
	if r := svc.Readings(context.Background(), testLat, testLon, SourceAgent); r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
	}

	if got := served(SourceAgent) - agentBefore; got != 1 {
		t.Fatalf("agent-sourced served count delta = %v, want 1", got)
	}
	if got := served(SourceTile) - tileBefore; got != 0 {
		t.Fatalf("tile served count moved by %v on an agent request, want 0", got)
	}
}
