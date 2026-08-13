package dashboard

import (
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/bloodpressure"
)

// Summary is the aggregate payload for GET /dashboard/summary. Each section
// is a nullable pointer so an absent domain (no runs, no workouts) serializes
// as JSON null rather than a zero-valued object, letting the client
// distinguish "no data" from "data that happens to be zero". Later tasks add
// the remaining sections (steps, nutrition, bodyweight, streak).
type Summary struct {
	Running    *RunningSection    `json:"running"`
	Lifting    *LiftingSection    `json:"lifting"`
	Steps      *StepsSection      `json:"steps"`
	Nutrition  *NutritionSection  `json:"nutrition"`
	Bodyweight *BodyweightSection `json:"bodyweight"`
	// Streak is a value, not a pointer: it is always meaningful (an empty
	// streak is zero, not "no data"), so it always serializes as an object.
	Streak StreakSection `json:"streak"`
	// Recovery is the Whoop recovery tile. Present (non-nil) only when the user
	// has a connected Whoop connection; nil otherwise so the card stays hidden.
	Recovery *RecoverySection `json:"recovery,omitempty"`
	// Sleep is the Whoop sleep tile. Present (non-nil) only when the user has a
	// connected Whoop connection; nil otherwise so the card stays hidden —
	// declared with omitempty exactly like Recovery.
	Sleep *SleepSection `json:"sleep,omitempty"`
	// BloodPressure is the blood-pressure tile. Present (non-nil) only when the
	// user has logged at least one reading; nil otherwise so the card stays
	// hidden — mirrors how Recovery is declared with omitempty.
	BloodPressure *BloodPressureSection `json:"blood_pressure,omitempty"`
	// Quote is the daily quote tile. A value rather than a pointer, like
	// Streak: it is served from an embedded corpus, so when the tile is enabled
	// it is always populated and there is no "no data" case to express.
	Quote QuoteSection `json:"quote"`
}

// QuoteSection is the daily quote tile's wire shape — the subset of a
// quotes.Quote the client renders. Tags stay server-side; they exist for future
// selection logic, not for display.
type QuoteSection struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Author string `json:"author"`
	// AuthorURL is the Wikipedia article for the author, omitted when there
	// is none to point at. The client renders the attribution as a link when
	// this is present and as plain text when it is not, so both states are
	// normal — this is not a field a client may assume.
	AuthorURL string `json:"author_url,omitempty"`
	// Source is the work the quote comes from, omitted when the attribution
	// has not been verified. See internal/quotes/data/README.md.
	Source string `json:"source,omitempty"`
	// SourceURL is the Wikipedia article for the work. Never present without
	// Source; the corpus loader rejects that combination.
	SourceURL string `json:"source_url,omitempty"`
	// Offset is the reroll position this quote was served at: 0 is the day's
	// quote, and the client sends back offset+1 to advance. Echoing it keeps
	// the client from having to track the count it asked for against the one
	// it got.
	Offset int `json:"offset"`
}

// SleepSection is the sleep tile. nil at the Summary level unless a connected
// Whoop connection exists — the same gate the recovery family uses.
type SleepSection struct {
	// LastNight is today's main sleep: nil when there is no non-nap record for
	// today. Deliberately not "the most recent night whenever it was" —
	// promoting a two-day-old night into today's slot is the bug the recovery
	// tile's no-reading branch exists to avoid.
	LastNight *SleepNight `json:"last_night"`
	// Nights is the date-aligned trailing window, oldest→newest: every date in
	// the window present, missing nights carrying null metrics. A night with no
	// data and a night of zero sleep are different facts.
	Nights []SleepNight `json:"nights"`
}

// SleepNight is one local date's main sleep. Date is always populated; every
// other field is nullable, because a date in the window with no record — or a
// record WHOOP has not scored — is represented, not omitted. Durations are
// milliseconds exactly as WHOOP sent them: the tile formats, the server does not
// convert. Field names match GET /whoop/sleep's sleepDTO so a client learns one
// vocabulary for both surfaces.
type SleepNight struct {
	Date string `json:"date"`

	InBedMilli         *int64 `json:"in_bed_milli"`
	AwakeMilli         *int64 `json:"awake_milli"`
	LightSleepMilli    *int64 `json:"light_sleep_milli"`
	SlowWaveSleepMilli *int64 `json:"slow_wave_sleep_milli"`
	RemSleepMilli      *int64 `json:"rem_sleep_milli"`
	NoDataMilli        *int64 `json:"no_data_milli"`
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
}

// RecoverySection is the Whoop recovery tile. nil at the Summary level unless a
// connected Whoop connection exists. Today and RestingHRSpark are unchanged in
// shape, semantics, and content; Days, Baseline, HRV, and BaselineTrend are
// additive.
//
// Two routes serve this shape — the dashboard summary over its fixed window and
// the recovery history read over a caller-supplied one — so a client maps it
// once. Days is the charted window; Baseline, HRV and BaselineTrend describe
// that window's FINAL day, which is today only while the window ends today.
// BaselineTrend additionally needs at least baseline_drift_days + 1 charted days
// to have a past to compare against, and reads "unknown" below that.
type RecoverySection struct {
	Today          *RecoveryDay          `json:"today"`            // nil when no row today
	RestingHRSpark []float64             `json:"resting_hr_spark"` // trailing 7 days resting HR (oldest→newest), missing days omitted
	Days           []RecoveryDayPoint    `json:"days"`             // full window, date-aligned oldest→newest, missing days present with null metrics
	Baseline       RecoveryBaseline      `json:"baseline"`         // trailing averages + HRV spread; always present
	HRV            RecoveryHRV           `json:"hrv"`              // today's HRV vs the user's own baseline; always present
	BaselineTrend  RecoveryBaselineTrend `json:"baseline_trend"`   // the baseline vs its own past; always present
}

// RecoveryDay is a single day's Whoop recovery snapshot for the tile. The three
// metric fields are nullable (Whoop may not have computed them) and serialize as
// JSON null.
type RecoveryDay struct {
	Date             string   `json:"date"`
	RestingHeartRate *float64 `json:"resting_heart_rate"`
	RecoveryScore    *float64 `json:"recovery_score"`
	HRVRmssdMilli    *float64 `json:"hrv_rmssd_milli"`
}

// RecoveryDayPoint is one charted day: the day's raw metrics (embedded, so the
// existing four keys serialize unchanged) plus the band as it stood on THAT
// day. The band fields are null and Status is "unknown" until the day has
// min_baseline_days behind it, so a client's band legitimately begins part-way
// across the series for a user with short history.
type RecoveryDayPoint struct {
	RecoveryDay
	BaselineAvg  *float64 `json:"baseline_avg"`
	BalancedLow  *float64 `json:"balanced_low"`
	BalancedHigh *float64 `json:"balanced_high"`
	ZScore       *float64 `json:"z_score"`
	Status       string   `json:"status"` // balanced | elevated | suppressed | unknown
}

// RecoveryBaseline carries the trailing averages and the spread behind the HRV
// band. Averages are null until their metric has min_baseline_days samples; the
// *Days counts are always populated so a client can render calibration progress.
type RecoveryBaseline struct {
	WindowDays        int      `json:"window_days"`
	RestingHRAvg      *float64 `json:"resting_hr_avg"`
	RestingHRDays     int      `json:"resting_hr_days"`
	HRVAvg            *float64 `json:"hrv_avg"`
	HRVStdDev         *float64 `json:"hrv_std_dev"`
	HRVDays           int      `json:"hrv_days"`
	RecoveryScoreAvg  *float64 `json:"recovery_score_avg"`
	RecoveryScoreDays int      `json:"recovery_score_days"`
}

// RecoveryHRV is today's HRV read against the user's own baseline. Bounds and
// z-score are derived from the same floored standard deviation, so they always
// agree with Status.
type RecoveryHRV struct {
	Status       string   `json:"status"` // balanced | elevated | suppressed | unknown
	BalancedLow  *float64 `json:"balanced_low"`
	BalancedHigh *float64 `json:"balanced_high"`
	ZScore       *float64 `json:"z_score"`
	Trend        string   `json:"trend"` // rising | falling | steady | unknown
	ShortAvg     *float64 `json:"short_avg"`
}

// RecoveryBaselineTrend is the baseline against its own past. NOT the same
// question as RecoveryHRV.Trend, which compares the recent mean against the
// window it sits inside; these two may point opposite ways. Always present —
// "nothing to say" is Direction "unknown", never an absent key.
type RecoveryBaselineTrend struct {
	Direction string   `json:"direction"` // rising | falling | steady | unknown
	DeltaMs   *float64 `json:"delta_ms"`
	FromAvg   *float64 `json:"from_avg"`
	OverDays  int      `json:"over_days"`
}

// RunningSection is the ONE shared payload every running-family tile reads
// (running / running_log / running_effort / running_vertical). nil at the
// Summary level when the user has no running activity at all.
type RunningSection struct {
	CurrentWeek RunningCurrentWeek `json:"current_week"`
	// Baseline is the trailing 4-week average EXCLUDING the current week —
	// what "normal" means for this athlete. nil until at least one prior
	// week holds a run.
	Baseline *RunningBaseline `json:"baseline"`
	// RecentAvgPaceSecPerKm is a 30-DAY aggregate — a different figure from
	// CurrentWeek.AvgPaceSecPerKm and labeled differently by every tile.
	RecentAvgPaceSecPerKm *float64   `json:"recent_avg_pace_sec_per_km"`
	LatestRun             *LatestRun `json:"latest_run"`
	// WeekRuns is this local week's runs, oldest→newest.
	WeekRuns []RunningWeekRun `json:"week_runs"`
	// WeeklyLoad is 8 week-anchored buckets, oldest→newest. A bucket with no
	// runs is a real zero — the distinction the bare spark could not make.
	WeeklyLoad []RunningWeekPoint `json:"weekly_load"`
	// WeeklyDistanceSpark is the legacy series the retired card read. It
	// survives this (expand) step because the deployed web build maps it
	// with no null guard; a follow-up contract PR deletes it once web has
	// stopped reading it.
	WeeklyDistanceSpark []float64 `json:"weekly_distance_spark"`
}

type RunningCurrentWeek struct {
	DistanceMeters      float64  `json:"distance_meters"`
	RunCount            int      `json:"run_count"`
	DeltaPctVsPriorWeek *float64 `json:"delta_pct_vs_prior_week"`
	DurationSeconds     int      `json:"duration_seconds"`
	// AvgPaceSecPerKm is the week AGGREGATE (Σduration / Σkm), not a mean of
	// per-run paces — the long run is exactly what separates the two.
	AvgPaceSecPerKm *float64 `json:"avg_pace_sec_per_km"`
	// AvgHeartRateBpm is duration-weighted over HR-bearing runs; nil when
	// none carry HR. HeartRateRuns says how many contributed.
	AvgHeartRateBpm *int `json:"avg_heart_rate_bpm"`
	// ElevationGainMeters sums gain over gain-bearing runs and is nil —
	// never 0 — when none carry it: an indoor-only week must stay
	// distinguishable from a flat week.
	ElevationGainMeters *float64 `json:"elevation_gain_meters"`
	HeartRateRuns       int      `json:"heart_rate_runs"`
	ElevationRuns       int      `json:"elevation_runs"`
	LongestRunMeters    float64  `json:"longest_run_meters"`
	// DaysRun counts distinct local dates run, 0–7.
	DaysRun int `json:"days_run"`
}

// RunningWeekRun is one of this week's runs, projected for the tiles.
type RunningWeekRun struct {
	ActivityID      string    `json:"activity_id"`
	Name            *string   `json:"name"`
	StartTime       time.Time `json:"start_time"`
	LocalDate       string    `json:"local_date"` // YYYY-MM-DD in the user's tz
	DistanceMeters  float64   `json:"distance_meters"`
	DurationSeconds int       `json:"duration_seconds"`
	AvgPaceSecPerKm *float64  `json:"avg_pace_sec_per_km"`
	AvgHeartRateBpm *int      `json:"avg_heart_rate_bpm"`
	// HeartRateZone is 1..5, nil unless the Run Effort tile is enabled (the
	// handler classifies against the max-HR reference; zone thresholds stay
	// single-sourced in Go rather than mirrored into TypeScript).
	HeartRateZone       *int     `json:"heart_rate_zone"`
	ElevationGainMeters *float64 `json:"elevation_gain_meters"`
	Environment         string   `json:"environment"` // outdoor | indoor
}

// RunningWeekPoint is one weekly bucket of the load rail.
type RunningWeekPoint struct {
	WeekStart           string   `json:"week_start"` // YYYY-MM-DD, local Monday
	DistanceMeters      float64  `json:"distance_meters"`
	DurationSeconds     int      `json:"duration_seconds"`
	RunCount            int      `json:"run_count"`
	ElevationGainMeters *float64 `json:"elevation_gain_meters"`
}

// RunningBaseline is the trailing 4-week average EXCLUDING the current week.
// The denominator is Weeks — weeks that actually held a run — not a flat 4,
// so a runner three weeks into the product isn't diluted by pre-signup zeros
// (SOW Open Question 2). Pace and HR are aggregates over the window's runs,
// the same method RecentAvgPaceSecPerKm uses, so the figures are comparable.
type RunningBaseline struct {
	WindowWeeks         int      `json:"window_weeks"` // 4
	Weeks               int      `json:"weeks"`        // weeks with >=1 run behind it
	DistanceMeters      *float64 `json:"distance_meters"`
	DurationSeconds     *int     `json:"duration_seconds"`
	AvgPaceSecPerKm     *float64 `json:"avg_pace_sec_per_km"`
	AvgHeartRateBpm     *int     `json:"avg_heart_rate_bpm"`
	ElevationGainMeters *float64 `json:"elevation_gain_meters"`
	RunsPerWeek         *float64 `json:"runs_per_week"`
}

// LatestRun is a thin projection of the user's most recent run. Name is
// nullable because activities can be imported without one.
type LatestRun struct {
	Name            *string   `json:"name"`
	DistanceMeters  float64   `json:"distance_meters"`
	DurationSeconds int       `json:"duration_seconds"`
	StartTime       time.Time `json:"start_time"`
}

// WalkingSection is the walking tile. nil at the Summary level when the user has
// no walking activity at all.
type WalkingSection struct {
	CurrentWeek         EnduranceCurrentWeek `json:"current_week"`
	LatestSession       *EnduranceLatest     `json:"latest_session"`
	WeeklyDistanceSpark []float64            `json:"weekly_distance_spark"`
}

// CyclingSection is the cycling tile. nil when the user has no cycling activity.
type CyclingSection struct {
	CurrentWeek         EnduranceCurrentWeek `json:"current_week"`
	LatestSession       *EnduranceLatest     `json:"latest_session"`
	WeeklyDistanceSpark []float64            `json:"weekly_distance_spark"`
}

// HikingSection is the hiking tile. nil when the user has no hiking activity.
// ElevationGainMeters is this week's summed elevation gain (Activity.ElevationGainMeters).
type HikingSection struct {
	CurrentWeek         EnduranceCurrentWeek `json:"current_week"`
	ElevationGainMeters float64              `json:"elevation_gain_meters"`
	LatestSession       *EnduranceLatest     `json:"latest_session"`
	WeeklyDistanceSpark []float64            `json:"weekly_distance_spark"`
}

// EnduranceCurrentWeek is the shared this-week rollup for the endurance tiles.
type EnduranceCurrentWeek struct {
	DistanceMeters  float64 `json:"distance_meters"`
	SessionCount    int     `json:"session_count"`
	DurationSeconds int     `json:"duration_seconds"`
}

// EnduranceLatest is a thin projection of the most recent session of a type.
type EnduranceLatest struct {
	Name            *string   `json:"name"`
	DistanceMeters  float64   `json:"distance_meters"`
	DurationSeconds int       `json:"duration_seconds"`
	StartTime       time.Time `json:"start_time"`
}

// LiftingSection is the lifting tile. nil at the Summary level when the user
// has logged no workouts.
type LiftingSection struct {
	CurrentWeek          LiftingCurrentWeek `json:"current_week"`
	HeadlineEstimated1RM *Headline1RM       `json:"headline_estimated_1rm"`
	// WeeklyVolumeSpark is ~8 weekly volume totals (Σ weight×reps),
	// oldest→newest, zero-filled for weeks without a session.
	WeeklyVolumeSpark []float64 `json:"weekly_volume_spark"`
	Unit              string    `json:"unit"`
}

type LiftingCurrentWeek struct {
	DurationSeconds int `json:"duration_seconds"`
	Sessions        int `json:"sessions"`
	Sets            int `json:"sets"`
	PRs             int `json:"prs"`
}

// Headline1RM is the user's flagship estimated one-rep max for the tile.
type Headline1RM struct {
	ExerciseName string  `json:"exercise_name"`
	Value        float64 `json:"value"`
	Unit         string  `json:"unit"`
}

// StepsSection is the steps tile. nil at the Summary level when the user has
// logged no step data at all.
type StepsSection struct {
	Avg   int `json:"avg"`
	Today int `json:"today"`
	// Goal is nil when no daily goal is set (serializes as null).
	Goal *int `json:"goal"`
	// DailySpark is the last 7 local calendar days oldest→newest, each day's
	// step count, zero-filled for days without an entry.
	DailySpark []int `json:"daily_spark"`
}

// NutritionSection is the nutrition tile. nil at the Summary level when there
// is no aggregate row for the local day.
type NutritionSection struct {
	Today NutritionMacros `json:"today"`
	// Goals is nil when no macro goals are set (serializes as null).
	Goals *NutritionGoals `json:"goals"`
}

type NutritionMacros struct {
	Calories float64 `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	CarbsG   float64 `json:"carbs_g"`
	FatG     float64 `json:"fat_g"`
}

type NutritionGoals struct {
	Calories int `json:"calories"`
	ProteinG int `json:"protein_g"`
	CarbsG   int `json:"carbs_g"`
	FatG     int `json:"fat_g"`
}

// BodyweightSection is the bodyweight tile. nil at the Summary level when the
// user has logged no measurements.
type BodyweightSection struct {
	Current float64 `json:"current"`
	Unit    string  `json:"unit"`
	// RatePerWeek is the least-squares trend slope scaled to per-week. nil
	// when it cannot be computed (<2 points or zero time span).
	RatePerWeek *float64 `json:"rate_per_week"`
	// Goal is nil when no goal is set (serializes as null).
	Goal *BodyweightGoal `json:"goal"`
	// TrendSpark is measured weights oldest→newest, downsampled to <=8.
	TrendSpark []float64 `json:"trend_spark"`
}

type BodyweightGoal struct {
	Weight float64 `json:"weight"`
	Unit   string  `json:"unit"`
}

// BloodPressureSection is the blood-pressure tile. nil at the Summary level
// when the user has logged no readings. The two sparks are computed from ONE
// day-bucketed series so their indices align — the card draws them as two
// lines on a shared x-axis.
type BloodPressureSection struct {
	Latest         BloodPressureLatest    `json:"latest"`
	Category       bloodpressure.Category `json:"category"`
	Avg30          *BloodPressureAvg      `json:"avg_30d"`
	SystolicSpark  []float64              `json:"systolic_spark"`
	DiastolicSpark []float64              `json:"diastolic_spark"`
}
type BloodPressureLatest struct {
	Systolic   int       `json:"systolic"`
	Diastolic  int       `json:"diastolic"`
	MeasuredAt time.Time `json:"measured_at"`
}
type BloodPressureAvg struct {
	Systolic  int `json:"systolic"`
	Diastolic int `json:"diastolic"`
}

// StreakSection is the training-streak tile. Always present (a value on
// Summary): an empty streak is a real, zero-valued state, not "no data".
type StreakSection struct {
	// Weeks is the run of consecutive active weeks counting backward.
	Weeks int `json:"weeks"`
	// ActiveDaysThisWeek is the count of active days in the current local week.
	ActiveDaysThisWeek int `json:"active_days_this_week"`
	// Week is the 7 days of the current local week, Mon→Sun, true when active.
	Week [7]bool `json:"week"`
}
