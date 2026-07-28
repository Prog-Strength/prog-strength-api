package activity

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Summary is the rendered card shape for one activity: the title, subtitle,
// and metric chips a list row or feed post displays. It is the same
// vocabulary the timeline hydrator's PostContent renders (minus the
// web-routing Href, which stays a wiring-layer concern) — descriptors
// produce Summaries so the hydrator and the unified /activities list share
// one card, not two divergent ones.
type Summary struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle"`
	Metrics  []string `json:"metrics"`
}

// RenderSummary renders one activity's card through its registered
// descriptor. details is the already-loaded typed detail value (nil is fine
// — every Summarize degrades to a base-row card). ok=false when reg is nil,
// the type is unregistered, or the descriptor has no Summarize.
func RenderSummary(reg *Registry, a Activity, details any) (Summary, bool) {
	if reg == nil {
		return Summary{}, false
	}
	d, err := reg.Lookup(a.ActivityType)
	if err != nil || d.Summarize == nil {
		return Summary{}, false
	}
	return d.Summarize(a, details), true
}

// RenderSummaries renders the card Summary for a batch of one user's
// activities, keyed by activity id. Detail loads are batched per type
// through the optional BulkDetailLoader capability (strength needs its
// exercises to count sets/volume; the endurance types summarize from the
// joined base row, so their stores skip the extra read entirely). Rendering
// is best-effort: types without a descriptor or Summarize are absent from
// the map, and a failed bulk load degrades that type to base-row cards — a
// summary problem never fails the caller's page. Shared by the unified
// /activities list and the timeline hydrator (aggregate-surfaces task).
func RenderSummaries(ctx context.Context, reg *Registry, userID string, activities []Activity) map[string]Summary {
	out := make(map[string]Summary, len(activities))
	if reg == nil {
		return out
	}
	details := LoadDetailsBulk(ctx, reg, userID, activities)
	for _, a := range activities {
		if s, ok := RenderSummary(reg, a, details[a.ID]); ok {
			out[a.ID] = s
		}
	}
	return out
}

// LoadDetailsBulk batch-loads the typed detail payloads for one user's
// activities through each type's optional BulkDetailLoader, keyed by
// activity id. Types whose stores don't implement the capability (the
// endurance types — their list summary reads off the joined base row) are
// simply absent. Best-effort like RenderSummaries: a failed bulk load
// degrades that type to no details rather than failing the caller's page.
// Shared by RenderSummaries and the unified list, which attaches the loaded
// payloads to its item DTOs (stage-3 /workouts parity) — one bulk read
// serves both, never a per-row fan-out.
func LoadDetailsBulk(ctx context.Context, reg *Registry, userID string, activities []Activity) map[string]any {
	details := make(map[string]any)
	if reg == nil {
		return details
	}
	idsByType := make(map[ActivityType][]string)
	for _, a := range activities {
		idsByType[a.ActivityType] = append(idsByType[a.ActivityType], a.ID)
	}
	for t, ids := range idsByType {
		d, err := reg.Lookup(t)
		if err != nil {
			continue
		}
		if bulk, ok := d.Details.(BulkDetailLoader); ok {
			loaded, err := bulk.LoadMany(ctx, userID, ids)
			if err != nil {
				log.Printf("activity summaries: bulk detail load for type %s failed: %v", t, err)
				continue
			}
			for id, v := range loaded {
				details[id] = v
			}
		}
	}
	return details
}

// The formatting helpers below are the card-chip vocabulary shared by the
// descriptors' Summarize renderers and the timeline hydrator. Display-only —
// the API itself stays metric.

// FormatMiles renders meters as a one-decimal mile string, e.g. "5.0 mi".
// metersPerMile comes from derivation.go.
func FormatMiles(meters float64) string {
	return fmt.Sprintf("%.1f mi", meters/metersPerMile)
}

// FormatDuration renders seconds as "M:SS" (or "H:MM:SS" past an hour),
// e.g. 2472s → "41:12".
func FormatDuration(seconds float64) string {
	total := int(seconds + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatThousands renders a whole-number volume with thousands separators,
// e.g. 8400 → "8,400".
func FormatThousands(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

// PluralCount renders "{n} {noun}" with a naive plural 's' for n != 1,
// e.g. 1 → "1 exercise", 3 → "3 exercises".
func PluralCount(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
