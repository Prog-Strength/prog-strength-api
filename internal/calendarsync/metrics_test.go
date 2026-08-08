package calendarsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
)

type stubConnLister struct {
	conns []calendarconn.Connection
	err   error
}

func (s stubConnLister) List(ctx context.Context) ([]calendarconn.Connection, error) {
	return s.conns, s.err
}

func tPtr(t time.Time) *time.Time { return &t }

func TestConnectionsExporter_PublishesCountsByStatus(t *testing.T) {
	e := NewConnectionsExporter(stubConnLister{conns: []calendarconn.Connection{
		{UserID: "a", Status: calendarconn.StatusConnected},
		{UserID: "b", Status: calendarconn.StatusConnected},
		{UserID: "c", Status: calendarconn.StatusRevoked},
	}})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("connected")); got != 2 {
		t.Fatalf("connected = %v, want 2", got)
	}
	if got := testutil.ToFloat64(connectionsGauge.WithLabelValues("revoked")); got != 1 {
		t.Fatalf("revoked = %v, want 1", got)
	}
}

func TestConnectionsExporter_PublishesNewestSyncAcrossConnectedUsers(t *testing.T) {
	older := time.Unix(1_700_000_000, 0)
	newer := time.Unix(1_700_009_999, 0)
	e := NewConnectionsExporter(stubConnLister{conns: []calendarconn.Connection{
		{UserID: "a", Status: calendarconn.StatusConnected, LastSuccessfulSyncAt: tPtr(older)},
		{UserID: "b", Status: calendarconn.StatusConnected, LastSuccessfulSyncAt: tPtr(newer)},
	}})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(lastSuccessfulSyncGauge); got != float64(newer.Unix()) {
		t.Fatalf("gauge = %v, want the newest stamp %v", got, newer.Unix())
	}
}

// A revoked connection is not syncing, so its stale stamp must not hold the
// liveness gauge up and mask a dead integration.
func TestConnectionsExporter_IgnoresRevokedConnectionsForFreshness(t *testing.T) {
	recent := time.Unix(1_700_009_999, 0)
	e := NewConnectionsExporter(stubConnLister{conns: []calendarconn.Connection{
		{UserID: "a", Status: calendarconn.StatusRevoked, LastSuccessfulSyncAt: tPtr(recent)},
	}})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(lastSuccessfulSyncGauge); got != 0 {
		t.Fatalf("gauge = %v, want 0 — only revoked connections have a stamp", got)
	}
}

// Publishing a literal 0 (rather than leaving the series absent) is what makes
// `time() - gauge` enormous, so the alert fires instead of silently passing.
func TestConnectionsExporter_PublishesZeroWhenNobodyHasEverSynced(t *testing.T) {
	e := NewConnectionsExporter(stubConnLister{conns: []calendarconn.Connection{
		{UserID: "a", Status: calendarconn.StatusConnected},
	}})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(lastSuccessfulSyncGauge); got != 0 {
		t.Fatalf("gauge = %v, want an explicit 0", got)
	}
}

func TestConnectionsExporter_PropagatesListErrors(t *testing.T) {
	e := NewConnectionsExporter(stubConnLister{err: errors.New("db down")})
	if err := e.refresh(context.Background()); err == nil {
		t.Fatal("refresh err = nil, want the list error propagated")
	}
}
