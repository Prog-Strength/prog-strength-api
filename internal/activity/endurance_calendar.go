package activity

import (
	"fmt"
	"strings"
)

// enduranceCalendarEvent renders the calendar manifestation for an
// endurance-shaped type. It is the Summarize card widened to the room an
// event body has: the same distance/time headline, plus the signals a card
// had to drop — pace, heart rate, elevation, a run's best efforts, and the
// session notes.
//
// Every section is conditional on the data existing. An indoor treadmill run
// with no HR strap renders headline-only, which is correct: an event body
// padded with "Elevation: 0 ft" would be noise, and nil already means
// "unknown / not applicable" throughout this package.
func enduranceCalendarEvent(t ActivityType) func(a Activity, details any) CalendarManifest {
	label := enduranceLabel(t)
	return func(a Activity, details any) CalendarManifest {
		d, _ := details.(*EnduranceDetails)

		m := CalendarManifest{
			Title:    label,
			Headline: enduranceHeadline(a, d),
		}
		if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
			m.Title = *a.Name
		}

		m.Sections = appendSection(m.Sections, "Heart rate", heartRateLines(a))
		m.Sections = appendSection(m.Sections, "Elevation", elevationLines(a, d))
		// Best efforts are a running-only product of the summarizer; the
		// slice is empty for every other type, so no type gate is needed.
		m.Sections = appendSection(m.Sections, "Best efforts", bestEffortLines(a))
		m.Sections = appendSection(m.Sections, "Notes", notesLines(a))
		return m
	}
}

// enduranceHeadline builds "5.2 mi · 41:12 · 7:55/mi". Distance prefers the
// loaded details (authoritative, and the calibrated value) and falls back to
// the joined base row, mirroring enduranceSummarize so the calendar and the
// card can never disagree about how far someone went.
func enduranceHeadline(a Activity, d *EnduranceDetails) string {
	meters := a.DistanceMeters
	if d != nil {
		meters = d.DistanceMeters
	}

	parts := make([]string, 0, 3)
	if meters > 0 {
		parts = append(parts, FormatMiles(meters))
	}
	if a.DurationSeconds > 0 {
		parts = append(parts, FormatDuration(float64(a.DurationSeconds)))
	}
	if pace := enduranceAvgPace(a, d); pace != nil && *pace > 0 {
		parts = append(parts, FormatPacePerMile(*pace))
	}
	return strings.Join(parts, " · ")
}

// enduranceAvgPace resolves average pace, preferring loaded details.
func enduranceAvgPace(a Activity, d *EnduranceDetails) *float64 {
	if d != nil && d.AvgPaceSecPerKm != nil {
		return d.AvgPaceSecPerKm
	}
	return a.AvgPaceSecPerKm
}

// heartRateLines renders the avg/max HR line, or nil when the source
// carried no heart rate at all.
func heartRateLines(a Activity) []string {
	var parts []string
	if a.AvgHeartRateBpm != nil {
		parts = append(parts, fmt.Sprintf("Avg %d bpm", *a.AvgHeartRateBpm))
	}
	if a.MaxHeartRateBpm != nil {
		parts = append(parts, fmt.Sprintf("Max %d bpm", *a.MaxHeartRateBpm))
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, " · ")}
}

// elevationLines renders gain/loss in feet. Gain prefers loaded details,
// matching the hiking card's resolution order.
//
// A zero gain is deliberately rendered when the field is present-but-zero:
// unlike a nil pointer that means "no altitude data", an explicit 0 on a
// track that HAD altitude is a real fact about a flat route.
func elevationLines(a Activity, d *EnduranceDetails) []string {
	gain, loss := a.ElevationGainMeters, a.ElevationLossMeters
	if d != nil {
		if d.ElevationGainMeters != nil {
			gain = d.ElevationGainMeters
		}
		if d.ElevationLossMeters != nil {
			loss = d.ElevationLossMeters
		}
	}

	var parts []string
	if gain != nil {
		parts = append(parts, "Gain "+formatGainFeet(*gain))
	}
	if loss != nil {
		parts = append(parts, fmt.Sprintf("Loss %s ft", FormatThousands(*loss*feetPerMeter)))
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, " · ")}
}

// bestEffortLines renders the fastest window per standard distance, e.g.
// "1 Mile   7:31". Iterating StandardDistances (rather than the activity's
// own slice) fixes the output to display order — shortest first — regardless
// of what order the sweep or the repository returned.
func bestEffortLines(a Activity) []string {
	if len(a.BestEfforts) == 0 {
		return nil
	}
	byKey := make(map[string]float64, len(a.BestEfforts))
	for _, be := range a.BestEfforts {
		byKey[be.DistanceKey] = be.DurationSeconds
	}

	var lines []string
	for _, sd := range StandardDistances {
		secs, ok := byKey[sd.Key]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s — %s", sd.DisplayName, FormatDuration(secs)))
	}
	return lines
}

// notesLines returns the session's free-text notes as body lines, split so a
// multi-line note keeps its shape in the event body.
func notesLines(a Activity) []string {
	if a.Notes == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*a.Notes)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// appendSection appends a section only when it has lines, so callers can
// chain every optional block without a conditional around each one.
func appendSection(dst []ManifestSection, heading string, lines []string) []ManifestSection {
	if len(lines) == 0 {
		return dst
	}
	return append(dst, ManifestSection{Heading: heading, Lines: lines})
}
