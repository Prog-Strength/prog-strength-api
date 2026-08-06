package whoopadmin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
)

// fakeLister is a connLister whose result (and error) the test controls.
type fakeLister struct {
	conns []whoopconn.Connection
	err   error
}

func (f *fakeLister) List(context.Context) ([]whoopconn.Connection, error) {
	return f.conns, f.err
}

func TestConnectionsExporter_RefreshSetsAllStatuses(t *testing.T) {
	fake := &fakeLister{conns: []whoopconn.Connection{
		{Status: whoopconn.StatusConnected},
		{Status: whoopconn.StatusConnected},
		{Status: whoopconn.StatusError},
	}}
	exp := NewConnectionsExporter(fake)

	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("connected")); got != 2 {
		t.Errorf("connected = %v, want 2", got)
	}
	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("error")); got != 1 {
		t.Errorf("error = %v, want 1", got)
	}
	// revoked has no connections but must still be published as 0 so a status
	// dropping to zero is observable rather than absent.
	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("revoked")); got != 0 {
		t.Errorf("revoked = %v, want 0", got)
	}
}

func TestConnectionsExporter_RefreshRecomputesDownward(t *testing.T) {
	fake := &fakeLister{conns: []whoopconn.Connection{
		{Status: whoopconn.StatusConnected},
		{Status: whoopconn.StatusConnected},
		{Status: whoopconn.StatusError},
	}}
	exp := NewConnectionsExporter(fake)

	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Fewer connections on the next pass must re-set the gauge, not add to it.
	fake.conns = []whoopconn.Connection{{Status: whoopconn.StatusConnected}}
	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("connected")); got != 1 {
		t.Errorf("connected = %v, want 1", got)
	}
	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("error")); got != 0 {
		t.Errorf("error = %v, want 0", got)
	}
}

// tptr is a time literal helper for the fixtures below.
func tptr(s string) *time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &ts
}

// TestConnectionsExporter_PublishesNewestWindowSyncAmongConnected pins the
// freshness gauge the dead-ingestion alert reads. It is a MAX over connected
// connections: the alert asks "is the webhook path alive at all", and the
// newest stamp answers that without a per-user label (the metrics in this
// integration are deliberately low-cardinality closed sets).
func TestConnectionsExporter_PublishesNewestWindowSyncAmongConnected(t *testing.T) {
	fake := &fakeLister{conns: []whoopconn.Connection{
		{Status: whoopconn.StatusConnected, LastWindowSyncAt: tptr("2026-08-05T15:09:29Z")},
		{Status: whoopconn.StatusConnected, LastWindowSyncAt: tptr("2026-08-01T01:00:00Z")},
	}}
	exp := NewConnectionsExporter(fake)

	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	want := float64(tptr("2026-08-05T15:09:29Z").Unix())
	if got := testutil.ToFloat64(lastWindowSyncGauge); got != want {
		t.Errorf("last window sync gauge = %v, want %v", got, want)
	}
}

// TestConnectionsExporter_IgnoresNonConnectedWindowSyncs pins that a revoked or
// errored connection's stale stamp cannot hold the gauge up. Those connections
// are not ingesting, so counting them would report liveness for a dead feed.
func TestConnectionsExporter_IgnoresNonConnectedWindowSyncs(t *testing.T) {
	fake := &fakeLister{conns: []whoopconn.Connection{
		{Status: whoopconn.StatusConnected, LastWindowSyncAt: tptr("2026-08-01T01:00:00Z")},
		{Status: whoopconn.StatusRevoked, LastWindowSyncAt: tptr("2026-08-05T15:09:29Z")},
		{Status: whoopconn.StatusError, LastWindowSyncAt: tptr("2026-08-06T06:00:00Z")},
	}}
	exp := NewConnectionsExporter(fake)

	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	want := float64(tptr("2026-08-01T01:00:00Z").Unix())
	if got := testutil.ToFloat64(lastWindowSyncGauge); got != want {
		t.Errorf("last window sync gauge = %v, want %v (non-connected rows must not count)", got, want)
	}
}

// TestConnectionsExporter_NoConnectedSyncsPublishesZero pins the never-synced
// case. Zero (rather than an absent series) is what makes the alert fire
// through `time() - gauge`: a connected account with no recorded window sync is
// a broken integration, not an unknown one. The connect-time seed in
// whoopconn.Upsert means this state should not arise in practice.
func TestConnectionsExporter_NoConnectedSyncsPublishesZero(t *testing.T) {
	fake := &fakeLister{conns: []whoopconn.Connection{
		{Status: whoopconn.StatusConnected, LastWindowSyncAt: nil},
		{Status: whoopconn.StatusRevoked, LastWindowSyncAt: tptr("2026-08-05T15:09:29Z")},
	}}
	exp := NewConnectionsExporter(fake)

	if err := exp.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if got := testutil.ToFloat64(lastWindowSyncGauge); got != 0 {
		t.Errorf("last window sync gauge = %v, want 0", got)
	}
}

func TestConnectionsExporter_RefreshReturnsListError(t *testing.T) {
	wantErr := errors.New("boom")
	exp := NewConnectionsExporter(&fakeLister{err: wantErr})

	if err := exp.refresh(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("refresh err = %v, want %v", err, wantErr)
	}
}
