package timeline

// SourceType is the closed set of source domains a timeline post can point
// at. It mirrors the CHECK constraint on timeline_post.source_type — adding
// a value silently here would let an EnsurePost pass Go validation and fail
// at the DB, so new values require both a code change and a migration.
type SourceType string

const (
	SourceWorkout    SourceType = "workout"
	SourceRun        SourceType = "run"
	SourcePR         SourceType = "pr"
	SourceBestEffort SourceType = "best_effort"
)

// Valid reports whether t is one of the known members. Callers validate any
// SourceType taken from untrusted input (a publisher hook, a backfill row)
// before persisting, since the DB CHECK is the only other backstop.
func (t SourceType) Valid() bool {
	switch t {
	case SourceWorkout, SourceRun, SourcePR, SourceBestEffort:
		return true
	}
	return false
}
