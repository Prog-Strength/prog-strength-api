package exercise

// MuscleGroup identifies a primary muscle targeted by an exercise.
type MuscleGroup string

const (
	MuscleChest      MuscleGroup = "chest"
	MuscleBack       MuscleGroup = "back"
	MuscleShoulders  MuscleGroup = "shoulders"
	MuscleBiceps     MuscleGroup = "biceps"
	MuscleTriceps    MuscleGroup = "triceps"
	MuscleForearms   MuscleGroup = "forearms"
	MuscleCore       MuscleGroup = "core"
	MuscleQuads      MuscleGroup = "quads"
	MuscleHamstrings MuscleGroup = "hamstrings"
	MuscleGlutes     MuscleGroup = "glutes"
	MuscleCalves     MuscleGroup = "calves"
)

// Valid reports whether m is a known MuscleGroup.
func (m MuscleGroup) Valid() bool {
	switch m {
	case MuscleChest, MuscleBack, MuscleShoulders, MuscleBiceps, MuscleTriceps,
		MuscleForearms, MuscleCore, MuscleQuads, MuscleHamstrings, MuscleGlutes,
		MuscleCalves:
		return true
	}
	return false
}
