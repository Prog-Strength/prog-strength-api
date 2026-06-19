package dashboard

import "time"

// Summary is the aggregate payload for GET /dashboard/summary. Each section
// is a nullable pointer so an absent domain (no runs, no workouts) serializes
// as JSON null rather than a zero-valued object, letting the client
// distinguish "no data" from "data that happens to be zero". Later tasks add
// the remaining sections (steps, nutrition, bodyweight, streak).
type Summary struct {
	Running *RunningSection `json:"running"`
	Lifting *LiftingSection `json:"lifting"`
}

// RunningSection is the running tile. nil at the Summary level when the user
// has no running activity at all.
type RunningSection struct {
	CurrentWeek           RunningCurrentWeek `json:"current_week"`
	RecentAvgPaceSecPerKm *float64           `json:"recent_avg_pace_sec_per_km"`
	LatestRun             *LatestRun         `json:"latest_run"`
	// WeeklyDistanceSpark is ~8 weekly distance_meters totals, oldest→newest,
	// zero-filled for weeks without a run.
	WeeklyDistanceSpark []float64 `json:"weekly_distance_spark"`
}

type RunningCurrentWeek struct {
	DistanceMeters      float64  `json:"distance_meters"`
	RunCount            int      `json:"run_count"`
	DeltaPctVsPriorWeek *float64 `json:"delta_pct_vs_prior_week"`
}

// LatestRun is a thin projection of the user's most recent run. Name is
// nullable because activities can be imported without one.
type LatestRun struct {
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
