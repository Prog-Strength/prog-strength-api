package whoopadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
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

func TestConnectionsExporter_RefreshReturnsListError(t *testing.T) {
	wantErr := errors.New("boom")
	exp := NewConnectionsExporter(&fakeLister{err: wantErr})

	if err := exp.refresh(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("refresh err = %v, want %v", err, wantErr)
	}
}
