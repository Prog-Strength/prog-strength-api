package dashboard

import "time"

// localWeekStart returns Monday 00:00:00 in loc for the local week containing
// t. A "week" is a user-local calendar concept: a session at 11pm local on
// Sunday belongs to that local week, not the UTC one, so the boundary is
// computed in loc (and via time.Date so it stays correct across DST shifts,
// which a fixed offset would get wrong).
func localWeekStart(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	// Go's Weekday() is Sunday=0..Saturday=6; ISO weeks start Monday.
	offset := (int(local.Weekday()) + 6) % 7
	y, mo, d := local.Date()
	return time.Date(y, mo, d-offset, 0, 0, 0, 0, loc)
}

// weeklyBucketStarts returns weeks Monday-00:00-local week starts in ascending
// order (oldest first), ending with the current week's Monday relative to now.
// Returns nil when weeks <= 0.
func weeklyBucketStarts(now time.Time, loc *time.Location, weeks int) []time.Time {
	if weeks <= 0 {
		return nil
	}
	current := localWeekStart(now, loc)
	starts := make([]time.Time, weeks)
	for i := 0; i < weeks; i++ {
		// AddDate over -7*n days keeps DST correctness vs. subtracting raw
		// hours: 7 calendar days isn't always 168 hours.
		starts[i] = current.AddDate(0, 0, -7*(weeks-1-i))
	}
	return starts
}

// downsampleFloats reduces xs to at most maxPoints values using an even stride
// while always retaining the first and last point (so a sparkline keeps its
// endpoints). xs is returned unchanged when it already fits, and empty/single
// inputs or a non-positive cap are handled without panicking.
func downsampleFloats(xs []float64, maxPoints int) []float64 {
	if maxPoints <= 0 {
		return nil
	}
	if len(xs) <= maxPoints {
		return xs
	}
	if maxPoints == 1 {
		return []float64{xs[len(xs)-1]}
	}

	out := make([]float64, 0, maxPoints)
	last := len(xs) - 1
	// Distribute maxPoints-1 intervals across the index range so the first
	// (i=0) and last (i=maxPoints-1) samples land exactly on the endpoints.
	for i := 0; i < maxPoints; i++ {
		idx := i * last / (maxPoints - 1)
		out = append(out, xs[idx])
	}
	return out
}
