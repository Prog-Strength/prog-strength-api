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
	// BloodPressure is the blood-pressure tile. Present (non-nil) only when the
	// user has logged at least one reading; nil otherwise so the card stays
	// hidden — mirrors how Recovery is declared with omitempty.
	BloodPressure *BloodPressureSection `json:"blood_pressure,omitempty"`
}

// RecoverySection is the Whoop recovery tile. nil at the Summary level unless a
// connected Whoop connection exists. Today and RestingHRSpark are unchanged in
// shape, semantics, and content; Days, Baseline, and HRV are additive.
type RecoverySection struct {
	Today          *RecoveryDay     `json:"today"`            // nil when no row today
	RestingHRSpark []float64        `json:"resting_hr_spark"` // trailing 7 days resting HR (oldest→newest), missing days omitted
	Days           []RecoveryDay    `json:"days"`             // full window, date-aligned oldest→newest, missing days present with null metrics
	Baseline       RecoveryBaseline `json:"baseline"`         // trailing averages + HRV spread; always present
	HRV            RecoveryHRV      `json:"hrv"`              // today's HRV vs the user's own baseline; always present
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
