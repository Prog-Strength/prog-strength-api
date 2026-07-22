package whoopsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// recoveryReadEnvelope mirrors the httpresp success shape with the recovery list DTO
// typed, so tests assert on the exact snake_case wire contract.
type recoveryReadEnvelope struct {
	Message string          `json:"message"`
	Data    recoveryListDTO `json:"data"`
}

func decodeRecovery(t *testing.T, rec *httptest.ResponseRecorder) recoveryListDTO {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env recoveryReadEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

func TestGetRecoveryReturnsOrderedRowsWithNullableNulls(t *testing.T) {
	d := newHandlerDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Seed three days for user-1: middle day has null metrics; a fourth day
	// belongs to user-2 (isolation) and an out-of-window day is excluded.
	seed := func(user, date string, rs, rhr, hrv *float64) {
		if err := d.rec.Upsert(ctx, whooprecovery.Entry{
			UserID: user, Date: date,
			RecoveryScore: rs, RestingHeartRate: rhr, HRVRmssdMilli: hrv,
			CycleID: 1, SleepID: user + "-" + date,
		}, now); err != nil {
			t.Fatalf("seed %s/%s: %v", user, date, err)
		}
	}
	fp := func(f float64) *float64 { return &f }
	seed("user-1", "2026-06-10", fp(70), fp(55), fp(40))
	seed("user-1", "2026-06-11", nil, nil, nil) // all-null metrics
	seed("user-1", "2026-06-12", fp(80), fp(50), fp(45))
	seed("user-1", "2026-06-20", fp(99), fp(99), fp(99)) // outside until
	seed("user-2", "2026-06-11", fp(11), fp(11), fp(11)) // other user

	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/recovery?since=2026-06-10&until=2026-06-12&timezone=America/Denver")
	got := decodeRecovery(t, rec).Recovery

	if len(got) != 3 {
		t.Fatalf("expected 3 rows in window, got %d: %+v", len(got), got)
	}
	// DESC order.
	wantDates := []string{"2026-06-12", "2026-06-11", "2026-06-10"}
	for i, w := range wantDates {
		if got[i].Date != w {
			t.Errorf("row %d date = %q, want %q", i, got[i].Date, w)
		}
	}
	// Middle row (2026-06-11) has null metrics.
	null := got[1]
	if null.RecoveryScore != nil || null.RestingHeartRate != nil || null.HRVRmssdMilli != nil {
		t.Errorf("null-metric row should serialize null pointers, got %+v", null)
	}
	// Newest row carries its values.
	if got[0].RecoveryScore == nil || *got[0].RecoveryScore != 80 {
		t.Errorf("recovery_score = %v, want 80", got[0].RecoveryScore)
	}
	if got[0].SleepID != "user-1-2026-06-12" {
		t.Errorf("sleep_id = %q, want user-1-2026-06-12", got[0].SleepID)
	}
}

func TestGetRecoveryEmptyWhenNoRows(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/recovery")
	got := decodeRecovery(t, rec)
	if got.Recovery == nil {
		t.Fatal("recovery should be an empty array, not null")
	}
	if len(got.Recovery) != 0 {
		t.Errorf("expected empty list, got %+v", got.Recovery)
	}
}

func TestGetRecoveryRejectsBadDate(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/recovery?since=not-a-date")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
