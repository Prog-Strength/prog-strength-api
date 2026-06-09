package usage

import "github.com/prometheus/client_golang/prometheus"

// queryDuration records wall-clock time for the cost-engine SUM queries
// (the two telemetry.db round trips behind SpendTodayUSD). No labels:
// the SOW pins this metric label-less, and there's a single query path.
// Observed in the ledger so it covers every caller, not just the handler.
var queryDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "api_usage_query_duration_seconds",
	Help:    "Wall-clock for the per-user daily-spend SUM queries against telemetry.db.",
	Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12),
})

// cappedTotal counts GET /me/usage responses that reported capped=true.
// A leading indicator of "users who can no longer use the product." No
// labels — never per-user (unbounded cardinality).
var cappedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_usage_capped_total",
	Help: "Count of GET /me/usage responses where the user was over their daily cap.",
})

func init() {
	prometheus.MustRegister(queryDuration, cappedTotal)
}
