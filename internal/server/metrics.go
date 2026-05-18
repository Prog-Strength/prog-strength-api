package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// httpRequestsTotal counts every HTTP request the API serves,
// labeled by method, the chi route pattern (NOT the raw URL, so
// /workouts/{id} stays one label instead of one per workout ID),
// and the response status code.
//
// Cardinality risk: chi's route pattern keeps URL params out of the
// label, so the cardinality is bounded by the number of routes
// (small) × methods × status codes. Safe at any traffic level.
var httpRequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "ps_http_requests_total",
		Help: "Total HTTP requests handled by the Prog Strength API, labeled by method, route, and status.",
	},
	[]string{"method", "route", "status"},
)

// httpRequestDuration records request latency in seconds, labeled
// like the counter above. Bucket boundaries cover the API's typical
// range (sub-millisecond up to ~10 seconds for the slowest paths).
var httpRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "ps_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, labeled by method, route, and status.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
	},
	[]string{"method", "route", "status"},
)

func init() {
	prometheus.MustRegister(httpRequestsTotal, httpRequestDuration)
}

// MetricsMiddleware records request count and latency against the
// Prometheus collectors above. Designed to wrap the chi router so
// it runs for every request after routing has resolved the pattern.
//
// The route pattern is pulled from chi's RouteContext — if a request
// did not match any registered route (404), the pattern is empty;
// we substitute "<unmatched>" so the metric still records the hit
// without inflating the label set with raw paths.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "<unmatched>"
		}
		labels := prometheus.Labels{
			"method": r.Method,
			"route":  route,
			"status": strconv.Itoa(ww.status),
		}
		httpRequestsTotal.With(labels).Inc()
		httpRequestDuration.With(labels).Observe(time.Since(start).Seconds())
	})
}

// MetricsHandler returns the Prometheus scrape endpoint. Mounted at
// /metrics in the router; the Caddy layer refuses to proxy this
// path to the public internet so only Docker-network scrape traffic
// reaches it.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// statusRecorder wraps a ResponseWriter to capture the status code
// for the latency/count labels. The default chi middleware.Logger
// uses a similar pattern; we don't reuse its WrapResponseWriter to
// keep the metrics path independent of the logger.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}
