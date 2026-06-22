package snapshot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// newTestHandler wires a Handler over all-empty (healthy) fakes and pins now
// to a fixed instant so the trailing-7-local-days default window is
// deterministic: 2026-06-21 12:00 UTC is 06:00 in America/Denver, so the
// local "today" is 2026-06-21 in that zone.
func newTestHandler() *Handler {
	svc := NewService(fakeWorkout{}, fakeExercise{}, fakeActivity{}, fakeSteps{},
		fakeBodyweight{}, fakeNutrition{}, fakeUser{u: &user.User{WeightUnit: user.WeightUnitPounds}})
	h := NewHandler(svc)
	h.now = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	return h
}

// snapshotEnvelope mirrors the {message,data} success envelope wrapping a
// Snapshot. Snapshot is reused directly since the test lives in-package.
type snapshotEnvelope struct {
	Message string   `json:"message"`
	Data    Snapshot `json:"data"`
}

func decodeSnapshot(t *testing.T, rec *httptest.ResponseRecorder) Snapshot {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

func doSnapshot(h *Handler, userID, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/training-snapshot"+query, nil)
	if userID != "" {
		req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	h.snapshot(rec, req)
	return rec
}

func TestSnapshot_DefaultWindowIsTrailing7(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "u1", "?timezone=America/Denver")
	snap := decodeSnapshot(t, rec)

	if snap.Period.Days != 7 {
		t.Errorf("period.days = %d, want 7", snap.Period.Days)
	}
	if snap.Period.Timezone != "America/Denver" {
		t.Errorf("period.timezone = %q, want America/Denver", snap.Period.Timezone)
	}
	// Trailing 7 local days ending 2026-06-21 (the pinned local "today").
	if snap.Period.StartDate != "2026-06-15" || snap.Period.EndDate != "2026-06-21" {
		t.Errorf("period = %+v, want 2026-06-15..2026-06-21", snap.Period)
	}
}

func TestSnapshot_ExplicitRangeHonored(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "u1", "?timezone=America/Denver&start_date=2026-06-01&end_date=2026-06-03")
	snap := decodeSnapshot(t, rec)

	if snap.Period.StartDate != "2026-06-01" || snap.Period.EndDate != "2026-06-03" {
		t.Errorf("period = %+v, want 2026-06-01..2026-06-03", snap.Period)
	}
	if snap.Period.Days != 3 {
		t.Errorf("period.days = %d, want 3", snap.Period.Days)
	}
}

func TestSnapshot_SingleDateHonored(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "u1", "?timezone=America/Denver&date=2026-06-10")
	snap := decodeSnapshot(t, rec)

	if snap.Period.StartDate != "2026-06-10" || snap.Period.EndDate != "2026-06-10" {
		t.Errorf("period = %+v, want single day 2026-06-10", snap.Period)
	}
	if snap.Period.Days != 1 {
		t.Errorf("period.days = %d, want 1", snap.Period.Days)
	}
}

func TestSnapshot_MissingTimezone400(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "u1", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSnapshot_InvalidTimezone400(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "u1", "?timezone=Not/AZone")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSnapshot_MissingAuthContext500(t *testing.T) {
	h := newTestHandler()
	rec := doSnapshot(h, "", "?timezone=America/Denver")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
