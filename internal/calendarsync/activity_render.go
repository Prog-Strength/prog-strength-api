package calendarsync

import (
	"strings"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
)

// completedMarker prefixes the summary of every logged session, so a glance at
// the calendar separates "this is what I did" from a planned workout's
// still-open time block.
const completedMarker = "✓ "

// minEventDuration is the floor applied to an event's length.
//
// DurationSeconds is nullable on the base row and genuinely zero for some
// manual logs. A zero-length Google event renders as an instantaneous tick
// that is nearly invisible in a week view, so the session would sync
// "successfully" and still look missing. Fifteen minutes is short enough not
// to misrepresent a quick session and long enough to be clickable.
const minEventDuration = 15 * time.Minute

// RenderActivityEvent turns an activity plus its rendered manifest into the
// Google event to write.
//
// The body is assembled here rather than in the activity domain because the
// framing — the branded header, the dividers, the footer link — is a property
// of the calendar surface, not of any activity type. A descriptor supplies
// content; this decides how a Prog Strength event looks.
//
// timezone is the user's IANA zone. It is carried alongside a UTC instant
// rather than instead of one: the activity happened at an absolute moment, and
// the zone only tells Google which wall clock to render it against.
func RenderActivityEvent(a *activity.Activity, m activity.CalendarManifest, timezone, appLinkBase string) GoogleEvent {
	start := a.StartTime.UTC()
	duration := time.Duration(a.DurationSeconds) * time.Second
	if duration < minEventDuration {
		duration = minEventDuration
	}

	ev := GoogleEvent{
		Summary:  completedMarker + m.Title,
		StartUTC: start,
		EndUTC:   start.Add(duration),
		Timezone: timezone,
	}

	var b strings.Builder
	b.WriteString("PROG STRENGTH · ")
	b.WriteString(activityKindLabel(a.ActivityType))
	b.WriteString("\n")
	b.WriteString(divider)

	if headline := strings.TrimSpace(m.Headline); headline != "" {
		b.WriteString("\n\n")
		b.WriteString(headline)
	}
	for _, sec := range m.Sections {
		lines := trimTrailingBlank(sec.Lines)
		if len(lines) == 0 {
			continue
		}
		b.WriteString("\n\n")
		if sec.Heading != "" {
			b.WriteString(sec.Heading)
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(lines, "\n"))
	}

	if link := activityLink(m.LinkPath, appLinkBase); link != "" {
		b.WriteString("\n\n")
		b.WriteString(divider)
		b.WriteString("\n")
		b.WriteString(link)
	}

	ev.Description = b.String()
	return ev
}

// activityKindLabel is the header's sport word. Unlike the card labels this
// reads as a category ("Strength Training", not "Workout"), because it sits
// under the product name in a header line.
func activityKindLabel(t activity.ActivityType) string {
	switch t {
	case activity.ActivityRunning:
		return "Run"
	case activity.ActivityWalking:
		return "Walk"
	case activity.ActivityCycling:
		return "Ride"
	case activity.ActivityHiking:
		return "Hike"
	case activity.ActivityStrengthTraining:
		return "Strength Training"
	}
	return "Activity"
}

// activityLink renders the footer deep link, or "" when no base URL is
// configured (the self-hosted / local case).
func activityLink(linkPath, appLinkBase string) string {
	base := strings.TrimRight(appLinkBase, "/")
	if base == "" || linkPath == "" {
		return ""
	}
	return "↗ Open in Prog Strength\n" + base + linkPath
}

// trimTrailingBlank drops trailing empty lines from a section body. The
// strength agenda emits a blank line BETWEEN exercise groups as a separator,
// which is correct mid-body and stray at the end — without this the event
// description would carry whitespace before every divider.
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}
