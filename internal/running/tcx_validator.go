package running

// Exported slug constants. The handler surfaces these to the client so a
// failed import shows a precise reason ("not a run", "no GPS distance")
// rather than a generic 400.
const (
	SlugParseFailed = "tcx_parse_failed"
	SlugNotRunning  = "tcx_not_running"
	SlugEmpty       = "tcx_empty"
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
// validationErr so the handler sees a uniform ValidationError. validate
// here covers the two semantic rejections.
func validate(p *parsedTCX) error {
	// Case-sensitive on purpose: Garmin emits exactly "Running". Accepting
	// variants would let walks/hikes ("Walking") slip in unintentionally.
	if p.Sport != "Running" {
		return validationErr(SlugNotRunning, "tcx sport is not Running: "+p.Sport)
	}

	// "Empty" means no usable track: a watch that recorded time but never
	// got a distance fix. Any single non-zero cumulative distance proves
	// the run covered ground.
	for _, tp := range p.Trackpoints {
		if tp.DistanceMeters > 0 {
			return nil
		}
	}
	return validationErr(SlugEmpty, "tcx has no trackpoint with non-zero distance")
}
