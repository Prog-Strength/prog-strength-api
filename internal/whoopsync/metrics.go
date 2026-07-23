package whoopsync

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the WHOOP integration. WHOOP pushes data to us
// (webhook-as-poke), so ingestion health is invisible to the usual
// request/response view — nobody is waiting on a page when a sync dies.
// These counters make "is ingestion alive?" a dashboard question, and they
// mirror the decision points the structured logs narrate (same philosophy as
// nutritionlookup's metrics): the counter shows the aggregate, and
// `filter request_id = "…"` in Logs Insights shows any single delivery's
// story.
//
// Cardinality: every label is a small closed set. The webhook `type` label is
// only ever populated from signature-verified WHOOP payloads (pre-verification
// failures use the literal "invalid"), so it is bounded by WHOOP's own event
// vocabulary.

// webhooksTotal counts webhook deliveries by event type and outcome:
//
//	synced / deleted     — handled successfully
//	sync_error / delete_error / route_error — failed; returned 500 so WHOOP retries
//	unknown_user         — no local connection for the WHOOP user id (dropped)
//	not_connected        — connection exists but is revoked/error (dropped)
//	ignored              — event type we don't handle (dropped)
//	bad_signature / bad_json — rejected before/after HMAC verification
var webhooksTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_webhooks_total",
		Help: "WHOOP webhook deliveries by event type and outcome.",
	},
	[]string{"type", "outcome"},
)

// syncsTotal counts sync attempts by kind (backfill on connect, window from a
// webhook nudge) and result. A healthy integration shows a steady trickle of
// {window, ok}; silence here while webhooksTotal climbs means syncs are dying.
var syncsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_syncs_total",
		Help: "WHOOP sync attempts by kind (backfill/window) and result (ok/error).",
	},
	[]string{"kind", "result"},
)

// syncRowsTotal counts recovery records processed by syncs, by disposition.
// upserted is the number that actually landed; the skipped_* series surface
// data-quality drift (WHOOP shipping unscorable records, cycles missing from
// the fetched window, undateable cycles) that otherwise only shows as warns.
var syncRowsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_sync_rows_total",
		Help: "WHOOP recovery records processed by syncs, by disposition (upserted/skipped_*).",
	},
	[]string{"disposition"},
)

// tokenRefreshesTotal counts refresh-grant attempts. invalid_grant means the
// user must reconnect (the connection was flipped to error); persist_error is
// the dangerous one — WHOOP rotated the token but we failed to store the new
// pair, which can orphan the connection.
var tokenRefreshesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_token_refreshes_total",
		Help: "WHOOP token refresh attempts by result (ok/invalid_grant/persist_error/error).",
	},
	[]string{"result"},
)

func init() {
	prometheus.MustRegister(
		webhooksTotal,
		syncsTotal,
		syncRowsTotal,
		tokenRefreshesTotal,
	)
}
