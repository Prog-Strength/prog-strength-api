package activity

// Exported slug constants. The handler surfaces these to the client so a
// failed import shows a precise reason ("not a parseable TCX", "no GPS
// distance") rather than a generic 400. SlugNotRunning is no longer
// emitted (the activity domain accepts any sport), but the constant is
// retained because external clients may still match on it.
const (
	SlugParseFailed = "tcx_parse_failed"
	SlugNotRunning  = "tcx_not_running"
	SlugEmpty       = "tcx_empty"
	// SlugNoEffortData rejects a strength TCX that carries neither heart-rate
	// samples nor calories — there is nothing to enrich the workout with.
	SlugNoEffortData = "tcx_no_effort_data"
)

// ValidationError carries a machine-readable Slug alongside the human
// message. The handler reads .Slug to pick a response; everything else
// treats it as a plain error. We use a typed error (not sentinel vars)
// because the slug is the payload the handler needs, and a struct keeps
// the message attached for logs.
type ValidationError struct {
	Slug string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Msg }

// validationErr is the single construction point so callers don't build
// ValidationError literals (and risk a typo'd slug) scattered around.
func validationErr(slug, msg string) error {
	return &ValidationError{Slug: slug, Msg: msg}
}

// validate runs the semantic checks against an already-parsed TCX. Parse
// failures are reported by parseTCX itself; the caller that parses then
// validates should wrap a parse error with SlugParseFailed via
// validationErr so the handler sees a uniform ValidationError.
//
// The prior sport=Running rejection has been removed: the activity domain
// accepts running, walking, cycling, and other sports, and the ingest
// pipeline classifies the sport via normalizeActivityType. The only
// semantic rejection left is "empty track" — a watch that recorded time
// but never got a distance fix is unusable regardless of sport.
func validate(p *parsedTCX) error {
	for _, tp := range p.Trackpoints {
		if tp.DistanceMeters > 0 {
			return nil
		}
	}
	return validationErr(SlugEmpty, "tcx has no trackpoint with non-zero distance")
}

// validateStrength is the distance-free counterpart used by the workout-TCX
// import/attach path. A strength session is stationary, so the run validator's
// "must have non-zero distance" rule would reject every valid file. The only
// thing worth rejecting here is a file with no effort signal at all: no HR
// samples AND no calories means there is nothing to put on the heart-rate &
// effort card. (HR-or-calories is accepted; the card degrades gracefully when
// one is missing.) Parse failures are reported by parseTCX itself.
func validateStrength(p *parsedTCX) error {
	hasHR := false
	for _, tp := range p.Trackpoints {
		if tp.HeartRateBpm != nil {
			hasHR = true
			break
		}
	}
	if !hasHR && !p.hasCalories {
		return validationErr(SlugNoEffortData, "tcx has no heart-rate samples and no calories")
	}
	return nil
}
