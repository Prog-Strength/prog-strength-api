package nutritionlookup

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the nutrition lookup service — the data behind
// the "Nutrition Lookup" Grafana dashboard (prog-strength-infra
// monitoring/grafana/dashboards/nutrition-lookup.json). Counters mirror
// the decision points the structured logs narrate, so the dashboard
// shows the aggregate and `filter request_id = "…"` in CloudWatch shows
// any single request's story.
//
// Cardinality: every label is a small closed set (outcomes, cache
// events, two provider sources × two results). Safe at any traffic.

// lookupRequestsTotal counts lookup requests by final disposition:
//
//	cache_hit    — served from a fresh cache row, no external calls
//	served       — providers answered (includes zero-match answers)
//	served_stale — every provider failed; expired cache row served
//	unavailable  — no provider configured (503 lookup_unavailable)
//	failed       — all providers errored, no usable cache (503 lookup_failed)
var lookupRequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_nutrition_lookup_requests_total",
		Help: "Nutrition lookup requests by final disposition.",
	},
	[]string{"outcome"},
)

// cacheEventsTotal counts cache-read dispositions:
// hit | miss | stale | corrupt | read_error. Hit rate =
// hit / (hit + miss + stale + corrupt).
var cacheEventsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_nutrition_lookup_cache_events_total",
		Help: "Nutrition lookup cache read events (hit/miss/stale/corrupt/read_error).",
	},
	[]string{"event"},
)

// cacheWritesTotal counts cache upserts by result (ok | error). Errors
// don't fail the lookup (the cache is an optimization) — which is
// exactly why they need a metric: nothing else surfaces a dying cache.
var cacheWritesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_nutrition_lookup_cache_writes_total",
		Help: "Nutrition lookup cache writes by result (ok/error).",
	},
	[]string{"result"},
)

// providerRequestsTotal counts external API calls by source
// (fatsecret | usda) and result (ok | error).
var providerRequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_nutrition_lookup_provider_requests_total",
		Help: "External nutrition provider calls by source and result.",
	},
	[]string{"source", "result"},
)

// providerDuration records external call latency per source. Buckets
// span 50ms–12.8s — beyond the 8s http.Client timeout so timeouts land
// in a bucket instead of +Inf.
var providerDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "api_nutrition_lookup_provider_duration_seconds",
		Help:    "External nutrition provider call latency in seconds, by source.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 9),
	},
	[]string{"source"},
)

func init() {
	prometheus.MustRegister(
		lookupRequestsTotal,
		cacheEventsTotal,
		cacheWritesTotal,
		providerRequestsTotal,
		providerDuration,
	)
}
