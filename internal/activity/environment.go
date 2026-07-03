package activity

// Environment distinguishes a GPS-recorded outdoor activity from an
// indoor/treadmill activity recorded without position. It is a general
// cross-cutting attribute — it applies to any distance-based activity type,
// not running-only — even though only running defaults to indoor at ingest.
type Environment string

const (
	EnvironmentOutdoor Environment = "outdoor"
	EnvironmentIndoor  Environment = "indoor"
)

// Valid reports whether e is a known member. Used to reject a bad value on
// the PATCH environment-override path before it reaches the DB CHECK.
func (e Environment) Valid() bool {
	switch e {
	case EnvironmentOutdoor, EnvironmentIndoor:
		return true
	}
	return false
}
