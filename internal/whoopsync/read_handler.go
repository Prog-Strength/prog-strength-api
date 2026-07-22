package whoopsync

import (
	"errors"
	"net/http"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// recoveryDateLayout is the wire + storage format for a recovery day. Recovery
// rows are keyed by local calendar date (derived at ingest), so the since/until
// query params are compared lexicographically — identical to steps.
const recoveryDateLayout = "2006-01-02"

// recoveryDTO is the wire shape of a single recovery day. It is defined
// explicitly (rather than serializing whooprecovery.Entry) so the JSON field
// names are the API's snake_case contract regardless of the repo struct. The
// three metric fields are pointers so a missing Whoop value serializes as null.
type recoveryDTO struct {
	Date             string    `json:"date"`
	RecoveryScore    *float64  `json:"recovery_score"`
	RestingHeartRate *float64  `json:"resting_heart_rate"`
	HRVRmssdMilli    *float64  `json:"hrv_rmssd_milli"`
	CycleID          int64     `json:"cycle_id"`
	SleepID          string    `json:"sleep_id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// recoveryListDTO is the wire shape for GET /whoop/recovery.
type recoveryListDTO struct {
	Recovery []recoveryDTO `json:"recovery"`
}

// getRecovery (authed) lists the caller's Whoop daily recovery rows within the
// optional [since, until] local-date window (both inclusive, YYYY-MM-DD). Rows
// are already keyed by local calendar date, so no timezone conversion is needed;
// a timezone param is accepted for MCP-contract consistency but ignored. Rows
// come back date-DESC (ListRange's order). No connection → empty list.
func (h *Handler) getRecovery(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	q := r.URL.Query()
	since := q.Get("since")
	until := q.Get("until")
	if since != "" {
		if err := validateRecoveryDate(since); err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid since (expected YYYY-MM-DD)")
			return
		}
	}
	if until != "" {
		if err := validateRecoveryDate(until); err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid until (expected YYYY-MM-DD)")
			return
		}
	}

	entries, err := h.rec.ListRange(r.Context(), userID, since, until)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list whoop recovery", err)
		return
	}

	out := recoveryListDTO{Recovery: make([]recoveryDTO, 0, len(entries))}
	for _, e := range entries {
		out.Recovery = append(out.Recovery, recoveryDTO{
			Date:             e.Date,
			RecoveryScore:    e.RecoveryScore,
			RestingHeartRate: e.RestingHeartRate,
			HRVRmssdMilli:    e.HRVRmssdMilli,
			CycleID:          e.CycleID,
			SleepID:          e.SleepID,
			CreatedAt:        e.CreatedAt,
			UpdatedAt:        e.UpdatedAt,
		})
	}
	httpresp.OK(w, "listed whoop recovery", out)
}

// validateRecoveryDate checks that s parses as a YYYY-MM-DD calendar date.
func validateRecoveryDate(s string) error {
	if _, err := time.Parse(recoveryDateLayout, s); err != nil {
		return errors.New("invalid date")
	}
	return nil
}
