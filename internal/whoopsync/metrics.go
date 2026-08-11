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

// syncsTotal counts sync attempts by domain (recovery/sleep), kind (backfill on
// connect, window from a webhook nudge) and result. A healthy integration shows
// a steady trickle of {recovery, window, ok}; silence here while webhooksTotal
// climbs means syncs are dying.
//
// The domain label was chosen over a parallel api_whoop_sleep_syncs_total
// counter deliberately: the two domains fail independently and must be readable
// apart, but one series keeps every existing dashboard query working with a
// label selector rather than a rename.
var syncsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_syncs_total",
		Help: "WHOOP sync attempts by domain (recovery/sleep), kind (backfill/window) and result (ok/error).",
	},
	[]string{"domain", "kind", "result"},
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

// sleepRowsTotal counts sleep records processed by syncs, by disposition
// (upserted, skipped_unscored, skipped_bad_date). It has no skipped_no_cycle
// series and cannot grow one: a sleep record carries its own timezone_offset,
// so the sleep path never joins to a cycle and that failure mode does not exist
// for it.
//
// Every disposition here counts ROWS, so `sum by (disposition)` is meaningful.
// That is why the scope skip is a separate counter below rather than a
// disposition: it happens once per sync, before any record is fetched.
//
// upserted and skipped_unscored deliberately overlap: an unscored record is
// still written (so the row exists when the score arrives) AND counted as
// unscored, so the dispositions do not sum to the records fetched.
var sleepRowsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_whoop_sleep_rows_total",
		Help: "WHOOP sleep records processed by syncs, by disposition (upserted/skipped_*).",
	},
	[]string{"disposition"},
)

// sleepScopeSkipsTotal counts syncs that skipped the sleep fetch outright
// because the connection never consented to read:sleep. It has no recovery
// counterpart — it is a user who has not reconnected since read:sleep was
// added, not an error, and watching it is how "users are silently not getting
// sleep" stays visible. Unlabelled and separate from sleepRowsTotal because the
// unit is a sync, not a row.
var sleepScopeSkipsTotal = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "api_whoop_sleep_scope_skips_total",
		Help: "WHOOP syncs that skipped the sleep fetch because the connection lacks read:sleep.",
	},
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
		sleepRowsTotal,
		sleepScopeSkipsTotal,
		tokenRefreshesTotal,
	)
}
