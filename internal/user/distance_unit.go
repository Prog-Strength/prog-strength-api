package user

// DistanceUnit identifies the unit a distance is recorded in.
// Lives in the user package because it's a user preference, mirroring
// WeightUnit. It governs how distance-based activity (e.g. running) is
// displayed for the user.
type DistanceUnit string

const (
	DistanceUnitMiles      DistanceUnit = "mi"
	DistanceUnitKilometers DistanceUnit = "km"
)

// Valid reports whether d is a known DistanceUnit.
func (d DistanceUnit) Valid() bool {
	switch d {
	case DistanceUnitMiles, DistanceUnitKilometers:
		return true
	}
	return false
}
