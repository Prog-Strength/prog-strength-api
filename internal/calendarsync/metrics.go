package calendarsync

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
)

// api_calendar_syncs_total counts every activity sync attempt.
//
// Labels are all closed sets, per this repo's convention:
//   - activity_type: the registered activity types
//   - trigger:       "inline" (a user action) or "reconcile" (a boot repair)
//   - result:        "synced" | "failed" | "skipped" | "reconnect_needed"
//
// The trigger split is what makes the dashboard diagnostic rather than merely
// reassuring. If inline writes silently break, total syncs stay healthy
// because the reconciler picks up the slack — and the operator learns nothing.
// Separating them turns "reconcile is doing all the work" into a visible
// signal.
//
// "skipped" is counted, not discarded: it is the dominant outcome (every
// activity from a user who never connected) and its absence would make a
// no-op deployment look identical to a broken one.
var syncsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_calendar_syncs_total",
		Help: "Google Calendar activity sync attempts by activity type, trigger, and result.",
	},
	[]string{"activity_type", "trigger", "result"},
)

// api_calendar_reconcile_runs_total counts reconciler passes by outcome, so a
// reconciler that is erroring out (or never running at all) is visible on its
// own rather than only through the absence of the repairs it would have made.
var reconcileRunsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_calendar_reconcile_runs_total",
		Help: "Calendar sync reconciler passes by result (ok/error).",
	},
	[]string{"result"},
)

// api_calendar_reconcile_backlog is the number of activities the last
// reconciler pass found still owing a write.
//
// It is a gauge, not a counter, and it is the early warning for the cap: a
// backlog that keeps growing pass over pass means the per-boot limit is too
// low for the write rate, and events are drifting further behind.
var reconcileBacklog = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "api_calendar_reconcile_backlog",
		Help: "Activities awaiting a Google Calendar write at the end of the last reconciler pass.",
	},
)

// api_calendar_connections gauges calendar connections by status. It gates
// the liveness alert — an empty or fully-disconnected install must not page
// for an integration nobody is using.
var connectionsGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "api_calendar_connections",
		Help: "Current count of Google Calendar connections by status.",
	},
	[]string{"status"},
)

// api_calendar_last_successful_sync_timestamp_seconds is the unix time of the
// most recent successful write across CONNECTED users — the series the
// dead-sync alert evaluates as `time() - <gauge> > threshold`.
//
// It reads DURABLE state (user_calendar_connection.last_successful_sync_at)
// rather than deriving from syncsTotal, and that distinction is the entire
// point. syncsTotal lives in process memory and resets on every deploy;
// increase() over it is last-minus-first, so a counter that goes 1 →
// (restart) → 1 reports zero change no matter how healthy the integration is.
// The WHOOP integration shipped exactly that alert and it fired continuously
// over working ingestion until migration 052 replaced it with a durable
// stamp. This gauge is that lesson applied before the outage instead of after.
//
// 0 means no connected user has ever synced. It is published as a literal
// zero rather than left absent so `time() - 0` yields an enormous age and the
// alert fires: a connected account that has never synced is broken, not
// unknown. Fail loud is the correct direction for a liveness monitor.
var lastSuccessfulSyncGauge = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "api_calendar_last_successful_sync_timestamp_seconds",
		Help: "Unix timestamp of the most recent successful calendar sync across connected users; 0 if none.",
	},
)

// api_calendar_event_reads_total counts every read of a user's calendar for
// the dashboard tile, by outcome and by whether the cache answered it.
//
// cache is split out because it is the only thing that makes the Google-call
// rate interpretable: a healthy tile serves most reads from the 60s TTL, and a
// sudden collapse in the cache-hit ratio means remounts, not usage.
var eventReadsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_calendar_event_reads_total",
		Help: "Google Calendar reads for the dashboard tile by result and cache outcome.",
	},
	[]string{"result", "cache"},
)

// api_calendar_event_read_seconds times the uncached read path.
//
// DELIBERATELY NO last-success stamp and no absence-of-success alert, in
// contrast to calendarconn.Connection.LastSuccessfulSyncAt. That stamp exists
// because the WRITE path is a background process whose silence is a symptom.
// This read path is USER-TRIGGERED: a day with no reads means nobody opened a
// dashboard, which is a fact about traffic, not a fault. Alerting on its
// absence would reproduce exactly the false-positive pattern that stamp was
// introduced to fix.
var eventReadDuration = prometheus.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "api_calendar_event_read_seconds",
		Help:    "Duration of an uncached Google Calendar read for the dashboard tile.",
		Buckets: prometheus.DefBuckets,
	},
)

// Cache label values, closed like every other label set here.
const (
	cacheHit  = "hit"
	cacheMiss = "miss"
)

// observeEventRead records one read. seconds is 0 for a read the cache or a
// short-circuit answered, and those are not timed — the histogram measures the
// Google round-trip, and folding in cache hits would flatter it into
// uselessness.
func observeEventRead(result, cache string, seconds float64) {
	eventReadsTotal.WithLabelValues(result, cache).Inc()
	if cache == cacheMiss && seconds > 0 {
		eventReadDuration.Observe(seconds)
	}
}

func init() {
	prometheus.MustRegister(
		syncsTotal, reconcileRunsTotal, reconcileBacklog,
		connectionsGauge, lastSuccessfulSyncGauge,
		eventReadsTotal, eventReadDuration,
	)
}

// MetricsObserver implements SyncObserver over the Prometheus counters. It
// keeps the service free of a Prometheus dependency and lets tests observe
// outcomes without touching a global registry.
type MetricsObserver struct{}

func (MetricsObserver) ObserveSync(activityType, trigger, result string) {
	syncsTotal.WithLabelValues(activityType, trigger, result).Inc()
}

// ObserveReconcileRun records one reconciler pass and the backlog it left.
func ObserveReconcileRun(result string, backlog int) {
	reconcileRunsTotal.WithLabelValues(result).Inc()
	reconcileBacklog.Set(float64(backlog))
}

// connectionRefreshInterval matches whoopadmin's cadence — these gauges feed
// alert gating, not a live view, so a five-minute refresh is ample and keeps
// a per-boot DB scan off the hot path.
const connectionRefreshInterval = 5 * time.Minute

// allStatuses is the closed set always published, so a status dropping to
// zero reads as 0 rather than leaving a stale non-zero sample behind.
var allStatuses = []calendarconn.Status{calendarconn.StatusConnected, calendarconn.StatusRevoked}

// connLister is the calendarconn read surface the exporter needs.
type connLister interface {
	List(ctx context.Context) ([]calendarconn.Connection, error)
}

// ConnectionsExporter periodically republishes the connection gauges and the
// durable freshness stamp.
type ConnectionsExporter struct {
	conns connLister
}

func NewConnectionsExporter(conns connLister) *ConnectionsExporter {
	return &ConnectionsExporter{conns: conns}
}

// Run refreshes immediately, then every connectionRefreshInterval, until ctx
// is cancelled.
func (e *ConnectionsExporter) Run(ctx context.Context) {
	if err := e.refresh(ctx); err != nil {
		slog.WarnContext(ctx, "calendarsync: connection gauge refresh failed", "error", err)
	}
	t := time.NewTicker(connectionRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.refresh(ctx); err != nil {
				slog.WarnContext(ctx, "calendarsync: connection gauge refresh failed", "error", err)
			}
		}
	}
}

func (e *ConnectionsExporter) refresh(ctx context.Context) error {
	conns, err := e.conns.List(ctx)
	if err != nil {
		return err
	}

	counts := map[calendarconn.Status]int{}
	var newest time.Time
	for _, c := range conns {
		counts[c.Status]++
		// Only connected rows count toward freshness. A revoked connection
		// is not syncing by definition, so letting its stale stamp hold the
		// gauge up would report liveness for a feed that is dead.
		if c.Status != calendarconn.StatusConnected || c.LastSuccessfulSyncAt == nil {
			continue
		}
		if c.LastSuccessfulSyncAt.After(newest) {
			newest = *c.LastSuccessfulSyncAt
		}
	}

	for _, s := range allStatuses {
		connectionsGauge.WithLabelValues(string(s)).Set(float64(counts[s]))
	}
	if newest.IsZero() {
		lastSuccessfulSyncGauge.Set(0)
	} else {
		lastSuccessfulSyncGauge.Set(float64(newest.Unix()))
	}
	return nil
}
