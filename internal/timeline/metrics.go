package timeline

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the timeline domain. Publishing a post into the feed
// index is best-effort and non-blocking to the source write (a workout save
// must never fail because the feed-index hiccupped), so a publish failure is
// swallowed from the user's perspective. That is exactly why it needs a
// metric: nothing else surfaces a silently-degrading feed index. The backfill
// can always repair a gap, but only if we know one opened.
//
// Cardinality: source_type is the four-member closed set from SourceType.
// Safe at any traffic.

// publishFailuresTotal counts EnsurePost failures from the write-path
// publisher, labelled by the source_type that failed to publish. A nonzero
// rate means posts are missing from the feed until the next backfill/reconcile.
var publishFailuresTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_timeline_publish_failures_total",
		Help: "Timeline EnsurePost (publish) failures by source_type; posts missing until reconcile.",
	},
	[]string{"source_type"},
)

func init() {
	prometheus.MustRegister(publishFailuresTotal)
}

// ObservePublishFailure increments the publish-failure counter for the given
// source_type. It is the exported seam the server-package publisher uses to
// meter a swallowed EnsurePost error, since the underlying CounterVec is
// unexported (the metric is owned by this domain, but the publish path that
// can fail lives in the wiring layer to avoid the source domains importing
// the timeline repository).
func ObservePublishFailure(sourceType SourceType) {
	publishFailuresTotal.WithLabelValues(string(sourceType)).Inc()
}
