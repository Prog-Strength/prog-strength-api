package activity

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/running/estimate"
)

// DemographicsLoader supplies profile fields for the max-effort engine. Optional
// and nil-safe — when unset the handler passes empty demographics.
type DemographicsLoader interface {
	LoadEstimateDemographics(ctx context.Context, userID string, now time.Time) estimate.Demographics
}

// assembleAttempts builds the v2 evidence set: one anchor per distance (current
// best) plus supporting history rows within HistoryMaxGapPct of that anchor.
func (h *Handler) assembleAttempts(ctx context.Context, userID string) ([]estimate.Attempt, error) {
	bests, err := h.repo.GetUserRunningBestEfforts(ctx, userID)
	if err != nil {
		return nil, err
	}
	anchorByKey := make(map[string]RunningBestEffort, len(bests))
	for _, b := range bests {
		anchorByKey[b.DistanceKey] = b
	}

	// Cache HR enrichment per activity within this request.
	hrCache := map[string]*float64{}

	var attempts []estimate.Attempt
	for _, d := range StandardDistances {
		points, err := h.repo.GetRunningBestEffortHistory(ctx, userID, d.Key)
		if err != nil {
			return nil, err
		}
		anchor, hasAnchor := anchorByKey[d.Key]
		for _, p := range points {
			isAnchor := hasAnchor && p.ActivityID == anchor.ActivityID
			if !isAnchor && hasAnchor {
				if !estimate.IncludeSupportingHistory(anchor.DurationSeconds, p.DurationSeconds) {
					continue
				}
			}
			a := pointToAttempt(d, p, isAnchor)
			if h.hrEngine != nil && p.WindowStartElapsed != nil && p.WindowEndElapsed != nil {
				if pct, ok := h.hrHighIntensityPct(ctx, userID, p, hrCache); ok {
					a.HRZoneHighIntensityPct = &pct
				}
			}
			attempts = append(attempts, a)
		}
	}
	return attempts, nil
}

func loggedBestAtDistance(attempts []estimate.Attempt, distanceKey string) *float64 {
	var best *float64
	for _, a := range attempts {
		if a.DistanceKey != distanceKey {
			continue
		}
		if best == nil || a.DurationSeconds < *best {
			s := a.DurationSeconds
			best = &s
		}
	}
	return best
}

func pointToAttempt(d StandardDistance, p BestEffortPoint, isAnchor bool) estimate.Attempt {
	return estimate.Attempt{
		DistanceKey:             d.Key,
		DistanceMeters:          d.Meters,
		DurationSeconds:         p.DurationSeconds,
		AchievedAt:              p.ActivityStartTime,
		ActivityDistanceMeters:  p.ActivityDistanceMeters,
		ActivityAvgPaceSecPerKm: p.ActivityAvgPaceSecPerKm,
		WindowStartElapsed:      p.WindowStartElapsed,
		WindowEndElapsed:        p.WindowEndElapsed,
		IsCurrentBestAtDistance: isAnchor,
	}
}

func (h *Handler) loadDemographics(ctx context.Context, userID string, now time.Time) estimate.Demographics {
	if h.demographicsLoader == nil {
		return estimate.Demographics{}
	}
	return h.demographicsLoader.LoadEstimateDemographics(ctx, userID, now)
}

// hrHighIntensityPct returns the fraction of effort-window time in HR zones
// 4–5. ok is false when HR data is unavailable.
func (h *Handler) hrHighIntensityPct(ctx context.Context, userID string, p BestEffortPoint, cache map[string]*float64) (float64, bool) {
	if cached, ok := cache[p.ActivityID]; ok {
		if cached == nil {
			return 0, false
		}
		return *cached, true
	}

	act, err := h.repo.Get(ctx, userID, p.ActivityID)
	if err != nil || len(act.Trackpoints) < 2 {
		cache[p.ActivityID] = nil
		return 0, false
	}

	tps := make([]hrzones.Trackpoint, 0, len(act.Trackpoints))
	currentSamples := make([]int, 0, len(act.Trackpoints))
	for _, tp := range act.Trackpoints {
		tps = append(tps, hrzones.Trackpoint{ElapsedSeconds: tp.ElapsedSeconds, HeartRateBpm: tp.HeartRateBpm})
		if tp.HeartRateBpm != nil {
			currentSamples = append(currentSamples, *tp.HeartRateBpm)
		}
	}
	stats, err := h.repo.RecentHRStats(ctx, userID, h.hrWindow, p.ActivityID)
	if err != nil {
		cache[p.ActivityID] = nil
		return 0, false
	}
	stats.CurrentRunP99 = hrzones.P99(currentSamples)
	ref := h.hrEngine.EstimateReference(stats)

	start := int(*p.WindowStartElapsed)
	end := int(*p.WindowEndElapsed)
	if end <= start {
		cache[p.ActivityID] = nil
		return 0, false
	}

	high := 0
	total := 0
	for i := 1; i < len(tps); i++ {
		prev, cur := tps[i-1], tps[i]
		if prev.HeartRateBpm == nil || cur.HeartRateBpm == nil {
			continue
		}
		segStart := prev.ElapsedSeconds
		segEnd := cur.ElapsedSeconds
		if segEnd <= start || segStart >= end {
			continue
		}
		clipStart, clipEnd := segStart, segEnd
		if clipStart < start {
			clipStart = start
		}
		if clipEnd > end {
			clipEnd = end
		}
		dt := clipEnd - clipStart
		if dt <= 0 {
			continue
		}
		total += dt
		mean := (float64(*prev.HeartRateBpm) + float64(*cur.HeartRateBpm)) / 2.0
		zone := h.hrEngine.ZoneForBPM(ref, int(mean+0.5))
		if zone >= 3 { // zones 4–5 (0-indexed 3, 4)
			high += dt
		}
	}
	if total == 0 {
		cache[p.ActivityID] = nil
		return 0, false
	}
	pct := float64(high) / float64(total)
	cache[p.ActivityID] = &pct
	return pct, true
}
