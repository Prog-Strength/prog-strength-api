package activity

// IngestSource tags how an Activity entered the system. It's stored on
// every row so a future Garmin Connect / Strava / Apple Health source
// can land alongside today's manual TCX uploads without ambiguity about
// where a row came from.
//
// The enum is closed: each new source requires a code change (a constant
// here and a new branch in any switch on IngestSource). Since migration 042
// there is no DB CHECK — Go owns the valid set.
type IngestSource string

const (
	// IngestManual is a manually logged session with no source file — every
	// strength workout created through POST /workouts. It carries no
	// source_activity_id, so it is exempt from the dedup index.
	IngestManual IngestSource = "manual"
	// IngestManualTCX is a user-uploaded TCX file (the only source wired
	// up today). The activity ID comes from the TCX <Id> element.
	IngestManualTCX IngestSource = "manual_tcx"
	// IngestGarminAPI is the planned Garmin Connect API source. The
	// normalizer panics on it until the integration is wired up — see
	// normalizeActivityType.
	IngestGarminAPI IngestSource = "garmin_api"
)

// Valid reports whether s is one of the known sources.
func (s IngestSource) Valid() bool {
	switch s {
	case IngestManual, IngestManualTCX, IngestGarminAPI:
		return true
	}
	return false
}
