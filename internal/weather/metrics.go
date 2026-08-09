package weather

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the weather dashboard tile — the data behind the
// "Weather Tile" Grafana dashboard and its budget alerts. Counters mirror
// the decision points the structured logs narrate; gauges are published by
// the Exporter (collector.go) from durable SQLite state.
//
// Cardinality: every label is a small closed set (outcomes, five endpoints,
// cache events, two-way results). Safe at any traffic.

// requestsTotal counts weather tile requests by final disposition:
//
//	cache_hit        — served from a fresh cache row, no external calls
//	served           — provider answered, cache refreshed; every section present and fresh
//	served_stale     — payload incomplete or partially old: a section served from an expired row or lost to a failed fetch (or a geocode reservation refused/failed with an expired row served)
//	budget_exhausted — readings reservation refused (whatever cache exists is attached), or a geocode reservation refused with no cached row
//	disabled         — feature flag off
//	failed           — provider errored, no usable cache
var requestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_requests_total",
		Help: "Weather tile requests by final disposition (cache_hit/served/served_stale/budget_exhausted/disabled/failed).",
	},
	[]string{"outcome"},
)

// providerCallsTotal counts external OpenWeather calls by endpoint
// (current | hourly | daily | geocode_direct | geocode_reverse) and result
// (ok | error). The sum across endpoints is the metered spend rate.
var providerCallsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_provider_calls_total",
		Help: "External OpenWeather calls by endpoint and result (ok/error).",
	},
	[]string{"endpoint", "result"},
)

// providerLatency records external call latency per endpoint. Buckets match
// nutritionlookup's provider histogram: 50ms–12.8s, beyond the HTTP client
// timeout so timeouts land in a bucket instead of +Inf.
var providerLatency = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "api_weather_provider_latency_seconds",
		Help:    "External OpenWeather call latency in seconds, by endpoint.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 9),
	},
	[]string{"endpoint"},
)

// cacheEventsTotal counts cache-read dispositions:
// hit | miss | stale | corrupt | read_error. Hit rate =
// hit / (hit + miss + stale + corrupt).
var cacheEventsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_cache_events_total",
		Help: "Weather cache read events (hit/miss/stale/corrupt/read_error).",
	},
	[]string{"event"},
)

// cacheWritesTotal counts cache upserts by result (ok | error). Errors don't
// fail the request (the cache is an optimization) — which is exactly why they
// need a metric: nothing else surfaces a dying cache.
var cacheWritesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_cache_writes_total",
		Help: "Weather cache writes by result (ok/error).",
	},
	[]string{"result"},
)

// The gauges below are read from durable SQLite state by the Exporter, never
// from process memory. That is deliberate: the WHOOP counter postmortem showed
// in-memory spend/liveness series reset on every restart and lie to alerts,
// so anything a budget alert evaluates must survive a deploy.

var callsUsedTodayGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_calls_used_today",
	Help: "OpenWeather calls reserved against today's UTC budget row, read from durable SQLite state (restart-proof).",
})

var dailyBudgetGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_daily_budget",
	Help: "The active ceiling, so dashboard thresholds are drawn from config, not hardcoded.",
})

var budgetUtilizationGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_budget_utilization_ratio",
	Help: "calls_used_today / daily_budget, from durable SQLite state; 0 when the ceiling is unset or misconfigured.",
})

var shutoffActiveGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_shutoff_active",
	Help: "1 when today's reserved spend has reached the active ceiling, else 0 (a multi-call reservation can be refused slightly before this flips).",
})

var lastSuccessGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_last_success_timestamp_seconds",
	Help: "Unix timestamp of the newest cached provider fetch, read from durable SQLite state (restart-proof); 0 if none.",
})

var enabledGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_enabled",
	Help: "1 when the weather feature flag is on, else 0 — lets alerts silence themselves while the tile is off.",
})

var savedLocationsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_saved_locations",
	Help: "Saved weather locations across all users, read from durable SQLite state.",
})

func init() {
	prometheus.MustRegister(
		requestsTotal,
		providerCallsTotal,
		providerLatency,
		cacheEventsTotal,
		cacheWritesTotal,
		callsUsedTodayGauge,
		dailyBudgetGauge,
		budgetUtilizationGauge,
		shutoffActiveGauge,
		lastSuccessGauge,
		enabledGauge,
		savedLocationsGauge,
	)
}
