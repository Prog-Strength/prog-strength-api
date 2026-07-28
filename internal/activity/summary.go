package activity

import (
	"fmt"
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

// The formatting helpers below are the card-chip vocabulary
// (server/timeline_hydrator.go currently carries private copies; the
// hydrator converges on these when it adopts Summarize). Display-only —
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
