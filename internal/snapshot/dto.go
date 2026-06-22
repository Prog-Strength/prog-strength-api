package snapshot

// Snapshot is the agent-facing, date-windowed, pre-aggregated view across
// every training domain. Each domain section is a pointer: nil ONLY when
// that domain's repository read failed (defensive degradation). An
// empty-but-healthy domain renders zeros/empty slices, never nil.
type Snapshot struct {
	Period      Period             `json:"period"`
	Strength    *StrengthSection   `json:"strength"`
	Running     *RunningSection    `json:"running"`
	Steps       *StepsSection      `json:"steps"`
	Bodyweight  *BodyweightSection `json:"bodyweight"`
	Nutrition   *NutritionSection  `json:"nutrition"`
	Consistency Consistency        `json:"consistency"`
}

type Period struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Timezone  string `json:"timezone"`
	Days      int    `json:"days"`
}

type StrengthSection struct {
	SessionCount  int                 `json:"session_count"`
	TotalVolume   float64             `json:"total_volume"`
	Unit          string              `json:"unit"`
	ByMuscleGroup []MuscleGroupVolume `json:"by_muscle_group"`
	Sessions      []StrengthSession   `json:"sessions"`
	HeadlinePRs   []string            `json:"headline_prs"`
}

type MuscleGroupVolume struct {
	MuscleGroup string  `json:"muscle_group"`
	Sets        int     `json:"sets"`
	Volume      float64 `json:"volume"`
}

type StrengthSession struct {
	Date    string      `json:"date"`
	Name    string      `json:"name"`
	TopSets []TopSet    `json:"top_sets"`
	PRs     []SessionPR `json:"prs"`
}

type TopSet struct {
	Exercise string  `json:"exercise"`
	Weight   float64 `json:"weight"`
	Reps     int     `json:"reps"`
	Est1RM   float64 `json:"est_1rm"`
}

type SessionPR struct {
	Exercise string `json:"exercise"`
	Kind     string `json:"kind"`
}

type RunningSection struct {
	RunCount        int          `json:"run_count"`
	TotalDistanceM  float64      `json:"total_distance_m"`
	TotalDurationS  int          `json:"total_duration_s"`
	AvgPaceSecPerKm *float64     `json:"avg_pace_sec_per_km"`
	Runs            []RunSummary `json:"runs"`
	NewBestEfforts  []BestEffort `json:"new_best_efforts"`
}

type RunSummary struct {
	Date            string   `json:"date"`
	Name            string   `json:"name"`
	DistanceM       float64  `json:"distance_m"`
	DurationS       int      `json:"duration_s"`
	AvgPaceSecPerKm *float64 `json:"avg_pace_sec_per_km"`
}

type BestEffort struct {
	DistanceKey string `json:"distance_key"`
	TimeSeconds int    `json:"time_seconds"`
}

type StepsSection struct {
	DaysLogged int        `json:"days_logged"`
	Avg        int        `json:"avg"`
	Total      int        `json:"total"`
	Goal       int        `json:"goal"`
	ByDay      []StepsDay `json:"by_day"`
}

type StepsDay struct {
	Date  string `json:"date"`
	Steps int    `json:"steps"`
}

type BodyweightSection struct {
	Unit     string              `json:"unit"`
	Start    float64             `json:"start"`
	End      float64             `json:"end"`
	Delta    float64             `json:"delta"`
	Readings []BodyweightReading `json:"readings"`
}

type BodyweightReading struct {
	Date   string  `json:"date"`
	Weight float64 `json:"weight"`
}

type NutritionSection struct {
	DaysLogged int            `json:"days_logged"`
	Avg        MacroSet       `json:"avg"`
	Goals      MacroSet       `json:"goals"`
	ByDay      []NutritionDay `json:"by_day"`
}

type MacroSet struct {
	Calories int `json:"calories"`
	ProteinG int `json:"protein_g"`
	FatG     int `json:"fat_g"`
	CarbsG   int `json:"carbs_g"`
}

type NutritionDay struct {
	Date     string `json:"date"`
	Calories int    `json:"calories"`
	ProteinG int    `json:"protein_g"`
	FatG     int    `json:"fat_g"`
	CarbsG   int    `json:"carbs_g"`
}

type Consistency struct {
	ActiveDays int `json:"active_days"`
	WindowDays int `json:"window_days"`
}
