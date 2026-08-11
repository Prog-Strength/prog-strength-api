package whoopsync

import (
	"net/http"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/daterange"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
)

// sleepDTO is the wire shape of a single WHOOP sleep record. It is defined
// explicitly (rather than serializing whoopsleep.Entry) so the snake_case JSON
// stays the API's contract independent of the repo struct — the field names
// below are what the web and MCP clients are written against, and a rename in
// the repository must not silently reshape them. Every score field is a pointer
// so a PENDING/UNSCORABLE record serializes null rather than a zero that reads
// as a real measurement. Durations are milliseconds exactly as WHOOP sent them.
type sleepDTO struct {
	WhoopSleepID   string `json:"whoop_sleep_id"`
	Date           string `json:"date"`
	IsNap          bool   `json:"is_nap"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at"`
	TimezoneOffset string `json:"timezone_offset"`
	ScoreState     string `json:"score_state"`

	InBedMilli         *int64 `json:"in_bed_milli"`
	AwakeMilli         *int64 `json:"awake_milli"`
	NoDataMilli        *int64 `json:"no_data_milli"`
	LightSleepMilli    *int64 `json:"light_sleep_milli"`
	SlowWaveSleepMilli *int64 `json:"slow_wave_sleep_milli"`
	RemSleepMilli      *int64 `json:"rem_sleep_milli"`
	SleepCycleCount    *int64 `json:"sleep_cycle_count"`
	DisturbanceCount   *int64 `json:"disturbance_count"`

	NeedBaselineMilli      *int64 `json:"need_baseline_milli"`
	NeedFromSleepDebtMilli *int64 `json:"need_from_sleep_debt_milli"`
	NeedFromStrainMilli    *int64 `json:"need_from_strain_milli"`
	NeedFromNapMilli       *int64 `json:"need_from_nap_milli"`

	RespiratoryRate *float64 `json:"respiratory_rate"`
	PerformancePct  *float64 `json:"performance_pct"`
	ConsistencyPct  *float64 `json:"consistency_pct"`
	EfficiencyPct   *float64 `json:"efficiency_pct"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// sleepListDTO is the wire shape for GET /whoop/sleep.
type sleepListDTO struct {
	Sleep []sleepDTO `json:"sleep"`
}

// getSleep (authed) lists the caller's WHOOP sleep records within the optional
// [since, until] local-date window (both inclusive, YYYY-MM-DD), newest first,
// one object per record with is_nap present so a client can filter.
//
// timezone is REQUIRED and validated, but nothing is converted with it. That
// looks redundant and is deliberate: rows are already keyed by the local WAKE
// date derived at ingest, so since/until compare lexicographically and no UTC
// bounds need constructing. Requiring the name anyway keeps the day-boundary
// contract the API's to own — a client must not learn that this endpoint works
// without a timezone, because a future re-dating (or any windowing that does
// need real bounds) would need it, and by then every caller would have dropped
// the param. Recovery accepts-and-ignores it for the same reason; here it is
// enforced.
func (h *Handler) getSleep(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	q := r.URL.Query()
	tz := q.Get("timezone")
	if tz == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	if _, err := daterange.LoadTimezone(tz); err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

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

	entries, err := h.sleep.ListRange(r.Context(), userID, since, until)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list whoop sleep", err)
		return
	}

	out := sleepListDTO{Sleep: make([]sleepDTO, 0, len(entries))}
	for _, e := range entries {
		out.Sleep = append(out.Sleep, toSleepDTO(e))
	}
	httpresp.OK(w, "listed whoop sleep", out)
}

// toSleepDTO maps one stored record onto the wire shape. The one name that
// differs is REMSleepMilli → rem_sleep_milli: the repo follows Go's initialism
// convention, the wire follows the payload's snake_case.
func toSleepDTO(e whoopsleep.Entry) sleepDTO {
	return sleepDTO{
		WhoopSleepID:           e.WhoopSleepID,
		Date:                   e.Date,
		IsNap:                  e.IsNap,
		StartedAt:              e.StartedAt,
		EndedAt:                e.EndedAt,
		TimezoneOffset:         e.TimezoneOffset,
		ScoreState:             e.ScoreState,
		InBedMilli:             e.InBedMilli,
		AwakeMilli:             e.AwakeMilli,
		NoDataMilli:            e.NoDataMilli,
		LightSleepMilli:        e.LightSleepMilli,
		SlowWaveSleepMilli:     e.SlowWaveSleepMilli,
		RemSleepMilli:          e.REMSleepMilli,
		SleepCycleCount:        e.SleepCycleCount,
		DisturbanceCount:       e.DisturbanceCount,
		NeedBaselineMilli:      e.NeedBaselineMilli,
		NeedFromSleepDebtMilli: e.NeedFromSleepDebtMilli,
		NeedFromStrainMilli:    e.NeedFromStrainMilli,
		NeedFromNapMilli:       e.NeedFromNapMilli,
		RespiratoryRate:        e.RespiratoryRate,
		PerformancePct:         e.PerformancePct,
		ConsistencyPct:         e.ConsistencyPct,
		EfficiencyPct:          e.EfficiencyPct,
		CreatedAt:              e.CreatedAt,
		UpdatedAt:              e.UpdatedAt,
	}
}
