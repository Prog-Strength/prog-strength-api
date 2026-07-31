package timeline

// SourceType is the closed set of source domains a timeline post can point
// at. It mirrors the CHECK constraint on timeline_post.source_type — adding
// a value silently here would let an EnsurePost pass Go validation and fail
// at the DB, so new values require both a code change and a migration.
//
// Deliberately coarse: `activity` covers EVERY training session — a lift, a
// run, a hike, a future kickboxing class — because the session's sport is
// already recoverable from activities.activity_type, and the Go type
// registry (internal/activity/registry.go) is the source of truth for that
// taxonomy. Splitting the sport into the source type would mean a table
// rebuild per new activity type (SQLite can't widen a CHECK in place) —
// exactly the churn migration 042 removed from the activities table and the
// reason a hike never reached the feed before migration 046. The two
// remaining members are genuinely different source domains, not sports:
// `pr` points at a personal_record_events row and `best_effort` at an
// activity_best_efforts row.
type SourceType string

const (
	// SourceActivity is any session in the unified activities table,
	// whatever its activity_type. Consumers that need the sport read the
	// post's hydrated ActivityType, not this discriminator.
	SourceActivity   SourceType = "activity"
	SourcePR         SourceType = "pr"
	SourceBestEffort SourceType = "best_effort"
)

// Valid reports whether t is one of the known members. Callers validate any
// SourceType taken from untrusted input (a publisher hook, a backfill row)
// before persisting, since the DB CHECK is the only other backstop.
func (t SourceType) Valid() bool {
	switch t {
	case SourceActivity, SourcePR, SourceBestEffort:
		return true
	}
	return false
}
