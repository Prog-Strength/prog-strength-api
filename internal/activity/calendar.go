package activity

import (
	"strings"
)

// CalendarManifest is one activity's Google Calendar manifestation: the
// provider-agnostic description of what the event should say. It is the
// calendar sibling of Summary — where Summary is the card vocabulary for
// feeds and list rows, this is the vocabulary for an event body, which has
// room for more than three chips.
//
// Deliberately free of Google specifics (no attendees, reminders, colors, or
// {dateTime,timeZone} shapes): the calendarsync package owns the translation
// from a manifest to a provider payload, so the activity domain never learns
// what a Google event looks like and a second provider needs no changes here.
// The event window is likewise absent — start and duration already live on
// the base Activity row, so repeating them in the manifest would create two
// sources of truth for the same fact.
type CalendarManifest struct {
	// Title is the event's one-line summary, e.g. "✓ Threshold Run".
	Title string
	// Headline is the single most important line of the body: what
	// happened, in the type's own terms ("5.2 mi · 41:12 · 7:55/mi").
	// Rendered directly under the title, above any sections.
	Headline string
	// Sections are ordered body blocks. A section with an empty Heading
	// renders as a bare block, which is how the Summary fallback emits a
	// subtitle without inventing a heading for it.
	Sections []ManifestSection
	// LinkPath is the app-relative path the event's "open in Prog Strength"
	// footer links to. Descriptors normally leave this empty and let
	// RenderManifest default it to the canonical /activities/{id}
	// redirector; an override exists for a type that one day wants to deep
	// link somewhere more specific.
	LinkPath string
}

// ManifestSection is one titled block of manifest body lines.
type ManifestSection struct {
	Heading string
	Lines   []string
}

// CanonicalLinkPath is the app-relative deep link for an activity. It
// deliberately points at a type-agnostic redirector rather than the
// per-type detail route (/running/{id}, /workouts/{id}, ...).
//
// The reason is that this URL is written into an EXTERNAL system and then
// outlives our control of it: Google keeps its copy of the event body
// indefinitely, and we cannot rewrite history when web routing changes. A
// direct /activities?view=cycling link baked into today's events would still
// be pointing at a list page long after a real /cycling/{id} detail page
// ships. Resolving the type at request time instead means every event ever
// written — including ones already sitting in someone's calendar — starts
// landing on the better page the moment it exists.
func CanonicalLinkPath(activityID string) string {
	return "/activities/" + activityID
}

// RenderManifest renders one activity's calendar manifestation through its
// registered descriptor. details is the already-loaded typed detail value
// (nil is fine — every renderer degrades to a base-row manifest).
//
// The fallback chain is what makes this safe to leave unimplemented:
//
//  1. the descriptor's own CalendarEvent, when it has one;
//  2. otherwise a manifest synthesized from the type's existing Summarize;
//  3. ok=false only when reg is nil, the type is unregistered, or the type
//     has neither renderer.
//
// Step 2 is the point. A newly registered activity type gets a correct,
// useful calendar event with zero calendar-specific code, the same way it
// already gets a feed card — which keeps the registry's "adding a type needs
// no change here" property intact. A type only writes a CalendarEvent when
// it has something a three-chip card cannot express (a lift's agenda, a
// run's best efforts).
//
// LinkPath is defaulted centrally so no descriptor has to know the URL
// scheme, and a descriptor that hardcoded one cannot silently drift from
// CanonicalLinkPath.
func RenderManifest(reg *Registry, a Activity, details any) (CalendarManifest, bool) {
	if reg == nil {
		return CalendarManifest{}, false
	}
	d, err := reg.Lookup(a.ActivityType)
	if err != nil {
		return CalendarManifest{}, false
	}

	var m CalendarManifest
	switch {
	case d.CalendarEvent != nil:
		m = d.CalendarEvent(a, details)
	case d.Summarize != nil:
		m = manifestFromSummary(d.Summarize(a, details))
	default:
		return CalendarManifest{}, false
	}

	if strings.TrimSpace(m.LinkPath) == "" {
		m.LinkPath = CanonicalLinkPath(a.ID)
	}
	return m, true
}

// manifestFromSummary adapts a card Summary into a manifest: the title
// carries over, the metric chips join into the headline (they are the card's
// "what happened"), and the subtitle becomes a bare section — but only when
// it says something the joined metrics do not.
//
// That last check matters more than it looks. Every endurance Summarize
// builds its Subtitle by joining its own Metrics with " · ", so copying both
// verbatim would print the identical line twice in every fallback event
// body. Comparing the two and dropping the duplicate keeps the common case
// clean while still preserving a genuinely distinct subtitle from a future
// type that writes one.
func manifestFromSummary(s Summary) CalendarManifest {
	headline := strings.Join(s.Metrics, " · ")
	m := CalendarManifest{Title: s.Title, Headline: headline}

	subtitle := strings.TrimSpace(s.Subtitle)
	if subtitle == "" || subtitle == headline {
		return m
	}
	if headline == "" {
		// Nothing else to lead with — promote the subtitle rather than
		// emitting a headless body.
		m.Headline = subtitle
		return m
	}
	m.Sections = []ManifestSection{{Lines: []string{subtitle}}}
	return m
}
