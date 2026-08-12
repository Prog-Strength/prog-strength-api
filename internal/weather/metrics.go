package weather

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the weather feature — the data behind the "Weather
// Tile" Grafana dashboard and its budget alerts. Counters mirror the decision
// points the structured logs narrate; gauges are published by the Exporter
// (collector.go) from durable SQLite state.
//
// Cardinality: every label is a small closed set (outcomes, two sources, five
// endpoints, cache events, two-way results). Safe at any traffic.

// requestsTotal counts weather requests by final disposition and by the
// surface that asked (see source.go). The two surfaces share one budget and
// one cache, so `source` splits attribution without pretending they are
// isolated; every other series in this file carries no `source` label for
// that reason.
//
// Disposition:
//
//	cache_hit        — served from a fresh cache row, no external calls
//	served           — provider answered, cache refreshed; every section present and fresh
//	served_stale     — payload incomplete or partially old: a section served from an expired row or lost to a failed fetch (or a geocode reservation refused/failed with an expired row served)
//	budget_exhausted — readings reservation refused (whatever cache exists is attached), or a geocode reservation refused with no cached row
//	disabled         — feature flag off
//	failed           — provider errored, no usable cache
//
// One HTTP request is usually one sample, but GET /weather?place= is two when
// the name is not already saved: the geocode and the readings each record their
// own disposition. A panel counting agent place lookups by request must expect
// that, or it will double-count them.
var requestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_requests_total",
		Help: "Weather requests by final disposition (cache_hit/served/served_stale/budget_exhausted/disabled/failed) and requesting surface (tile/agent).",
	},
	[]string{"outcome", "source"},
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
// hit | miss | stale | corrupt | outdated | read_error. Hit rate =
// hit / (hit + miss + stale + corrupt + outdated).
//
// `outdated` is a row that PARSED but predates a payload shape change — it
// carries less than the current model promises, so it is refetched like a miss.
// It is deliberately not `corrupt`: corrupt means a row nobody wrote correctly
// and is worth alerting on, while outdated is the expected one-off cost of a
// deploy that widened a payload, and burying it in `corrupt` would make a
// schema change look like a cache fault.
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

// activityCapturesTotal counts activity_weather capture attempts by outcome:
//
//	ok             — a reading was fetched and stored
//	no_coordinates — terminal, no provider call: the activity recorded no position
//	unavailable    — terminal: the provider has no reading for that hour
//	failed         — transient (provider error, broken ledger, failed write); no row
//
// A budget refusal is deliberately absent from that set — activity_capture.go
// records why: a backfill stopping on the day's ceiling is a clean stop, and
// `failed` is the band an operator is meant to read as "stop and look".
var activityCapturesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_weather_activity_captures_total",
		Help: "Activity weather capture attempts by result (ok/no_coordinates/unavailable/failed).",
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

// The two activity gauges are published by the Exporter from durable SQLite
// for the same reason as the budget gauges above, and one more: "how much
// history is left to backfill" is only meaningful if it survives a restart.
//
// Both count LIVE activities only. The repository's CountOK joins activities
// and drops soft-deleted ones so it shares CountPending's universe — otherwise
// captured + pending would not describe the same set of activities and the two
// stat panels would quietly disagree.
var activitiesWithWeatherGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_activities_with_weather",
	Help: "Live (non-deleted) activities with a stored 'ok' weather reading, read from durable SQLite state.",
})

// Pending counts only the activities a backfill would spend a call on: an
// outdoor activity with a start coordinate and no row. Activities with no GPS
// are resolved for free and are excluded, so this gauge trending to zero is
// backfill progress rather than a number with an unreachable floor.
var activitiesPendingCaptureGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_weather_activities_pending_capture",
	Help: "Outdoor GPS activities still owed a weather capture (one provider call each), read from durable SQLite state; zero means the backfill is complete.",
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
		activityCapturesTotal,
		activitiesWithWeatherGauge,
		activitiesPendingCaptureGauge,
	)
}
