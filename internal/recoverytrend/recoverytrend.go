package recoverytrend

import "math"

// Config groups the recovery-trend tunables. All are non-secret public
// literals sourced from the [recovery] block of config.toml.
type Config struct {
	BaselineWindowDays int     // trailing days, EXCLUDING today, behind every average
	MinBaselineDays    int     // samples required before an average is emitted
	TrendWindowDays    int     // recent days, INCLUDING today, behind the trend
	MinTrendDays       int     // samples required in that window before a trend is emitted
	BalancedZ          float64 // |z| within this many SDs of baseline reads as balanced
	TrendZ             float64 // recent mean must sit this many SDs off baseline to read rising/falling
	MinStdDevMs        float64 // SD floor, so a near-flat history cannot divide by ~0
}

// HRV status values. Directional on purpose: elevated and suppressed are
// materially different signals a two-state client can merge but never recover.
const (
	StatusBalanced   = "balanced"
	StatusElevated   = "elevated"
	StatusSuppressed = "suppressed"
	StatusUnknown    = "unknown"
)

// HRV trend directions.
const (
	TrendRising  = "rising"
	TrendFalling = "falling"
	TrendSteady  = "steady"
	TrendUnknown = "unknown"
)

// Day is one local calendar day of recovery metrics. Date is YYYY-MM-DD; each
// metric is nil when Whoop had no reading for that day. These are the engine's
// own value types — the dashboard package maps whooprecovery.Entry into them.
type Day struct {
	Date          string
	RestingHR     *float64
	HRV           *float64
	RecoveryScore *float64
}

// Baseline carries the trailing averages and the HRV spread. Averages are nil
// until their metric has MinBaselineDays non-null samples; the *Days counts are
// always populated so a client can render calibration progress.
type Baseline struct {
	WindowDays        int
	RestingHRAvg      *float64
	RestingHRDays     int
	HRVAvg            *float64
	HRVStdDev         *float64
	HRVDays           int
	RecoveryScoreAvg  *float64
	RecoveryScoreDays int
}

// HRV is today's HRV read against the user's own baseline. Bounds and z-score
// derive from the same floored standard deviation, so they always agree with
// Status.
type HRV struct {
	Status       string
	BalancedLow  *float64
	BalancedHigh *float64
	ZScore       *float64
	Trend        string
	ShortAvg     *float64
}

// Engine computes recovery baselines and HRV balance from a fixed Config.
type Engine struct{ cfg Config }

// New returns an Engine bound to cfg.
func New(cfg Config) *Engine { return &Engine{cfg: cfg} }

// BaselineWindowDays is the trailing local-day count (excluding today) behind
// every baseline average. The dashboard read path uses it to size the window it
// fetches and the days slice it materializes, so the engine stays the single
// source of the window width.
func (e *Engine) BaselineWindowDays() int { return e.cfg.BaselineWindowDays }

// Compute derives the baseline and HRV blocks from a date-ascending window of
// daily rows whose FINAL element is today. Pure: no clock, no DB, no I/O.
func (e *Engine) Compute(days []Day) (Baseline, HRV) {
	b := Baseline{WindowDays: e.cfg.BaselineWindowDays}
	h := HRV{Status: StatusUnknown, Trend: TrendUnknown}
	if len(days) == 0 {
		return b, h
	}
	return b, h // math filled in Task 2
}

// collect returns the non-nil values of one metric across days, in order.
func collect(days []Day, sel func(Day) *float64) []float64 {
	out := make([]float64, 0, len(days))
	for _, d := range days {
		if v := sel(d); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// mean is the arithmetic mean of xs; callers guarantee len(xs) > 0.
func mean(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// stdDevPop is the population standard deviation (divide by n) of xs about m.
func stdDevPop(xs []float64, m float64) float64 {
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)))
}

// lastN returns the final n elements of days (all of them when n >= len).
func lastN(days []Day, n int) []Day {
	if n >= len(days) {
		return days
	}
	return days[len(days)-n:]
}
