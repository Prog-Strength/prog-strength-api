package activity

// IngestSource tags how an Activity entered the system. It's stored on
// every row so a future Garmin Connect / Strava / Apple Health source
// can land alongside today's manual TCX uploads without ambiguity about
// where a row came from.
//
// The enum is closed: each new source requires a code change (a constant
// here, a CHECK-constraint migration, and a new branch in any switch on
// IngestSource). That trade-off is deliberate — silently accepting an
// unknown source would let inserts pass Go validation and fail at the DB.
type IngestSource string

const (
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
	case IngestManualTCX, IngestGarminAPI:
		return true
	}
	return false
}
