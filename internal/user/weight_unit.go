package user

// WeightUnit identifies the unit a weight is recorded in.
// Lives in the user package because it's a user preference, though
// it's also stored alongside each workout set to preserve the original
// entry without conversion drift.
type WeightUnit string

const (
	WeightUnitPounds    WeightUnit = "lb"
	WeightUnitKilograms WeightUnit = "kg"
)

// Valid reports whether w is a known WeightUnit.
func (w WeightUnit) Valid() bool {
	switch w {
	case WeightUnitPounds, WeightUnitKilograms:
		return true
	}
	return false
}
