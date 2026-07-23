package whoopsync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Metrics are process-global counters, so these tests assert DELTAS around the
// action rather than absolute values — they stay correct regardless of what
// other tests in the package incremented first.

func TestWebhookMetrics_SyncedAndUnknownUser(t *testing.T) {
	h := newWebhookHarness(t)
	synced := webhooksTotal.WithLabelValues("recovery.updated", "synced")
	unknown := webhooksTotal.WithLabelValues("recovery.updated", "unknown_user")
	syncedBefore := testutil.ToFloat64(synced)
	unknownBefore := testutil.ToFloat64(unknown)

	ts := h.tsNow()
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	if rec := h.do(t, sign(h.secret, ts, body), ts, body); rec.Code >= 300 {
		t.Fatalf("synced delivery status = %d, want 2xx", rec.Code)
	}
	// WHOOP user 777 has no local connection → dropped as unknown_user.
	body = []byte(`{"user_id":777,"type":"recovery.updated","id":"sleep-2"}`)
	if rec := h.do(t, sign(h.secret, ts, body), ts, body); rec.Code >= 300 {
		t.Fatalf("unknown-user delivery status = %d, want 2xx", rec.Code)
	}

	if got := testutil.ToFloat64(synced) - syncedBefore; got != 1 {
		t.Errorf("synced counter delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(unknown) - unknownBefore; got != 1 {
		t.Errorf("unknown_user counter delta = %v, want 1", got)
	}
}

func TestWebhookMetrics_BadSignature(t *testing.T) {
	h := newWebhookHarness(t)
	c := webhooksTotal.WithLabelValues("invalid", "bad_signature")
	before := testutil.ToFloat64(c)

	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	if rec := h.do(t, "garbage-signature", h.tsNow(), body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	if got := testutil.ToFloat64(c) - before; got != 1 {
		t.Errorf("bad_signature counter delta = %v, want 1", got)
	}
}

// TestSyncMetrics_RowDispositionsAndResult mirrors the fixture from
// TestSyncWindow_UpsertsOnlyScoredSkipsMissingCycle (1 upserted, 2 unscored,
// 1 missing-cycle) and asserts each disposition and the ok result land in the
// counters the Grafana panels read.
func TestSyncMetrics_RowDispositionsAndResult(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles: []Cycle{
			{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"},
			{ID: 2, Start: "2026-01-16T02:00:00Z", TimezoneOffset: "-08:00"},
			{ID: 3, Start: "2026-01-17T12:00:00Z", TimezoneOffset: "-08:00"},
		},
		recoveries: []Recovery{
			{CycleID: 1, SleepID: "s1", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72)}},
			{CycleID: 2, SleepID: "s2", ScoreState: "PENDING"},
			{CycleID: 3, SleepID: "s3", ScoreState: "UNSCORABLE"},
			{CycleID: 99, SleepID: "s4", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(80)}},
		},
	}

	upserted := syncRowsTotal.WithLabelValues("upserted")
	unscored := syncRowsTotal.WithLabelValues("skipped_unscored")
	noCycle := syncRowsTotal.WithLabelValues("skipped_no_cycle")
	windowOK := syncsTotal.WithLabelValues("window", "ok")
	deltas := map[string]float64{
		"upserted": testutil.ToFloat64(upserted),
		"unscored": testutil.ToFloat64(unscored),
		"no_cycle": testutil.ToFloat64(noCycle),
		"ok":       testutil.ToFloat64(windowOK),
	}

	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })
	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}

	if got := testutil.ToFloat64(upserted) - deltas["upserted"]; got != 1 {
		t.Errorf("upserted delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(unscored) - deltas["unscored"]; got != 2 {
		t.Errorf("skipped_unscored delta = %v, want 2", got)
	}
	if got := testutil.ToFloat64(noCycle) - deltas["no_cycle"]; got != 1 {
		t.Errorf("skipped_no_cycle delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(windowOK) - deltas["ok"]; got != 1 {
		t.Errorf("syncs{window,ok} delta = %v, want 1", got)
	}
}
