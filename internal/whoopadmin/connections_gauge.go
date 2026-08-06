package whoopadmin

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
)

// api_whoop_connections gauges connection health by status, refreshed every
// refreshInterval. It gates the dead-ingestion alert (an empty DB must not
// page) and gives the ps-whoop dashboard a connection-health panel.
var connectionsGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "api_whoop_connections",
		Help: "Current count of WHOOP connections by status.",
	},
	[]string{"status"},
)

// api_whoop_last_window_sync_timestamp_seconds is the unix time of the most
// recent webhook-driven window sync across CONNECTED connections — the series
// the dead-ingestion alert evaluates as `time() - <gauge> > threshold`.
//
// Why a gauge sourced from the DB rather than the obvious
// increase(api_whoop_syncs_total{kind="window",result="ok"}[36h]): that counter
// lives in process memory and resets on every API restart. WHOOP nudges us
// roughly once a day and the container restarts about as often, so the counter
// typically sat at 1 for a whole process lifetime. increase() reads
// last-minus-first, and 1 → (restart) → 1 is neither a rise nor a decrease that
// reset-detection would correct, so it evaluated to 0 and the alert fired
// permanently over healthy ingestion. Reading durable state makes the signal
// independent of process lifetime. The counter stays — it is still the right
// thing for the dashboard's per-kind sync rates.
//
// No user_id label, deliberately: the alert asks whether the webhook path is
// alive at all, and every other metric in this integration keeps its labels to
// small closed sets. A max across connections answers that question; per-user
// freshness is the admin surface's job, not an alerting series'.
//
// 0 means no connected connection has a recorded window sync. Published as a
// literal zero rather than left absent so the alert's subtraction yields a huge
// age and fires — a connected account that has never ingested is broken, not
// unknown. whoopconn.Upsert seeds the stamp at connect, so this should not
// occur outside tests.
var lastWindowSyncGauge = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "api_whoop_last_window_sync_timestamp_seconds",
		Help: "Unix timestamp of the most recent webhook-driven WHOOP window sync across connected connections; 0 if none.",
	},
)

func init() { prometheus.MustRegister(connectionsGauge, lastWindowSyncGauge) }

const refreshInterval = 5 * time.Minute

// allStatuses is the closed set always published, so a status dropping to zero
// shows as 0 rather than a stale non-zero sample lingering.
var allStatuses = []whoopconn.Status{whoopconn.StatusConnected, whoopconn.StatusRevoked, whoopconn.StatusError}

// connLister is the whoopconn read surface the exporter needs.
type connLister interface {
	List(ctx context.Context) ([]whoopconn.Connection, error)
}

// ConnectionsExporter periodically publishes connectionsGauge from whoopconn.List.
type ConnectionsExporter struct {
	conns connLister
}

func NewConnectionsExporter(conns connLister) *ConnectionsExporter {
	return &ConnectionsExporter{conns: conns}
}

// Run refreshes immediately, then every refreshInterval, until ctx is cancelled.
func (e *ConnectionsExporter) Run(ctx context.Context) {
	if err := e.refresh(ctx); err != nil {
		slog.WarnContext(ctx, "whoopadmin: connection gauge refresh failed", "error", err)
	}
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.refresh(ctx); err != nil {
				slog.WarnContext(ctx, "whoopadmin: connection gauge refresh failed", "error", err)
			}
		}
	}
}

func (e *ConnectionsExporter) refresh(ctx context.Context) error {
	conns, err := e.conns.List(ctx)
	if err != nil {
		return err
	}
	counts := map[whoopconn.Status]int{}
	var newestSync time.Time
	for _, c := range conns {
		counts[c.Status]++
		// Only connected rows count: a revoked/errored connection is not
		// ingesting, so letting its stale stamp hold the gauge up would report
		// liveness for a feed that is definitionally dead.
		if c.Status != whoopconn.StatusConnected || c.LastWindowSyncAt == nil {
			continue
		}
		if c.LastWindowSyncAt.After(newestSync) {
			newestSync = *c.LastWindowSyncAt
		}
	}
	for _, s := range allStatuses {
		connectionsGauge.WithLabelValues(string(s)).Set(float64(counts[s]))
	}
	if newestSync.IsZero() {
		lastWindowSyncGauge.Set(0)
	} else {
		lastWindowSyncGauge.Set(float64(newestSync.Unix()))
	}
	return nil
}
