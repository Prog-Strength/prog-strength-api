package plannedworkout

import "time"

// sameLocalDay reports whether the plan's scheduled start and the session start
// fall on the same calendar day rendered in the plan's own IANA timezone. A plan
// with an unloadable timezone never matches.
func sameLocalDay(plan PlannedWorkout, sessionStartUTC time.Time) bool {
	loc, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return false
	}
	ps := plan.ScheduledStartUTC.In(loc)
	ss := sessionStartUTC.In(loc)
	py, pm, pd := ps.Date()
	sy, sm, sd := ss.Date()
	return py == sy && pm == sm && pd == sd
}

// selectPlan returns the candidate plan of the wanted ActivityKind that the
// session at sessionStartUTC completes, or nil. Candidates are planned-status
// plans of the matching kind on the same local day; the winner is the one whose
// ScheduledStartUTC is closest to the session start, breaking ties by earliest
// ScheduledStartUTC then by ID (fully deterministic; exact ties are effectively
// impossible in practice). The caller derives wantKind from the completing
// activity's activity_type (strength_training → lift, endurance → run).
func selectPlan(plans []PlannedWorkout, sessionStartUTC time.Time, wantKind ActivityKind) *PlannedWorkout {
	var best *PlannedWorkout
	var bestDelta time.Duration
	for i := range plans {
		p := plans[i]
		if p.Status != StatusPlanned || p.ActivityKind != wantKind || p.DeletedAt != nil {
			continue
		}
		if !sameLocalDay(p, sessionStartUTC) {
			continue
		}
		delta := p.ScheduledStartUTC.Sub(sessionStartUTC)
		if delta < 0 {
			delta = -delta
		}
		if best == nil || delta < bestDelta ||
			(delta == bestDelta && (p.ScheduledStartUTC.Before(best.ScheduledStartUTC) ||
				(p.ScheduledStartUTC.Equal(best.ScheduledStartUTC) && p.ID < best.ID))) {
			b := p
			best = &b
			bestDelta = delta
		}
	}
	return best
}
