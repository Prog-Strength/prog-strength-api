package estimate

// Evidence-policy helpers shared by the handler when assembling Attempt
// slices and by unit tests. The engine itself is pure: it trusts whatever
// attempts the caller passes in.

// HistoryMaxGapPct is how much slower than the per-distance anchor a
// supporting history row may be and still enter the fit (3% slower).
const HistoryMaxGapPct = 0.03

// IncludeSupportingHistory reports whether a non-anchor history row at the
// same distance should participate. When there is no anchor, every row is
// included (cold start at that distance).
func IncludeSupportingHistory(anchorSeconds, candidateSeconds float64) bool {
	if anchorSeconds <= 0 {
		return true
	}
	if candidateSeconds <= 0 {
		return false
	}
	return candidateSeconds <= anchorSeconds*(1+HistoryMaxGapPct)
}
