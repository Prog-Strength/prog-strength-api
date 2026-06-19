package dashboard

import "time"

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
