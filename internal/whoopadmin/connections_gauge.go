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

func init() { prometheus.MustRegister(connectionsGauge) }

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
	for _, c := range conns {
		counts[c.Status]++
	}
	for _, s := range allStatuses {
		connectionsGauge.WithLabelValues(string(s)).Set(float64(counts[s]))
	}
	return nil
}
