package dashboard

import (
	"math"
	"sort"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bloodpressure"
)

// bpSparkMax is the maximum number of points in each blood-pressure sparkline.
const bpSparkMax = 8

// buildBloodPressure assembles the blood-pressure tile from the user's
// readings. Pure. loc buckets readings into local calendar days for the
// daily-average sparks; now anchors the trailing-30-day average window.
// Returns nil when there are no readings. The two sparks come from ONE
// day-bucketed series so their indices and length align.
func buildBloodPressure(entries []bloodpressure.Entry, now time.Time, loc *time.Location) *BloodPressureSection {
	if len(entries) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	newest := entries[0]
	for i := range entries {
		if entries[i].MeasuredAt.After(newest.MeasuredAt) {
			newest = entries[i]
		}
	}
	type dayAgg struct{ sysSum, diaSum, n int }
	byDay := map[string]*dayAgg{}
	var order []string
	for _, e := range entries {
		key := e.MeasuredAt.In(loc).Format("2006-01-02")
		a := byDay[key]
		if a == nil {
			a = &dayAgg{}
			byDay[key] = a
			order = append(order, key)
		}
		a.sysSum += e.Systolic
		a.diaSum += e.Diastolic
		a.n++
	}
	sort.Strings(order)
	sysDaily := make([]float64, len(order))
	diaDaily := make([]float64, len(order))
	for i, k := range order {
		a := byDay[k]
		sysDaily[i] = math.Round(float64(a.sysSum) / float64(a.n))
		diaDaily[i] = math.Round(float64(a.diaSum) / float64(a.n))
	}
	cutoff := now.AddDate(0, 0, -30)
	var sSum, dSum, n int
	for _, e := range entries {
		if e.MeasuredAt.Before(cutoff) {
			continue
		}
		sSum += e.Systolic
		dSum += e.Diastolic
		n++
	}
	var avg30 *BloodPressureAvg
	if n > 0 {
		avg30 = &BloodPressureAvg{
			Systolic:  int(math.Round(float64(sSum) / float64(n))),
			Diastolic: int(math.Round(float64(dSum) / float64(n))),
		}
	}
	return &BloodPressureSection{
		Latest:         BloodPressureLatest{Systolic: newest.Systolic, Diastolic: newest.Diastolic, MeasuredAt: newest.MeasuredAt},
		Category:       newest.Category(),
		Avg30:          avg30,
		SystolicSpark:  downsampleFloats(sysDaily, bpSparkMax),
		DiastolicSpark: downsampleFloats(diaDaily, bpSparkMax),
	}
}
