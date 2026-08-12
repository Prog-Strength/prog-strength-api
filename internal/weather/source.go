package weather

// Source names the surface a weather request came from. The set is closed
// because the value becomes a Prometheus label: the tile and the agent share
// one provider budget, one cache, and one daily ceiling, and the only new
// operational question this feature creates is "is chat eating the tile's
// budget?".
type Source string

const (
	SourceTile  Source = "tile"
	SourceAgent Source = "agent"
)

// ParseSource resolves the ?source= query value. An absent value is the tile:
// that surface shipped first, so defaulting this way is what lets the existing
// web client keep working unedited. An unrecognized value is rejected rather
// than coerced to the default — a metric label taken from arbitrary caller
// input is an unbounded-cardinality hazard.
func ParseSource(raw string) (Source, bool) {
	switch src := Source(raw); src {
	case "":
		return SourceTile, true
	case SourceTile, SourceAgent:
		return src, true
	default:
		return "", false
	}
}
