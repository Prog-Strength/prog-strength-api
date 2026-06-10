package exercise

// MovementPattern is a coarse, training-oriented grouping of muscle
// groups (push / pull / legs / core / all). The Progress page collapses
// the twelve muscle-group chips down to these five patterns; the
// pattern → muscle-group rollup is defined here, in one place, so the
// HTTP handler and any future caller resolve it identically.
//
// See prog-strength-docs/sows/progress-page-modernization.md.
type MovementPattern string

const (
	MovementPush MovementPattern = "push"
	MovementPull MovementPattern = "pull"
	MovementLegs MovementPattern = "legs"
	MovementCore MovementPattern = "core"
	MovementAll  MovementPattern = "all"
)

// AllMuscleGroups returns every catalog muscle group in a stable,
// defined order. The order is the canonical anatomical ordering used
// throughout the UI (chest → back → arms → core → legs) so a caller
// rendering "Showing chest, shoulders, triceps" gets a predictable
// caption. A fresh slice is returned on each call so callers can't
// mutate shared state.
func AllMuscleGroups() []MuscleGroup {
	return []MuscleGroup{
		MuscleChest,
		MuscleBack,
		MuscleShoulders,
		MuscleBiceps,
		MuscleTriceps,
		MuscleForearms,
		MuscleCore,
		MuscleQuads,
		MuscleHamstrings,
		MuscleGlutes,
		MuscleCalves,
	}
}

// MovementPatternMuscleGroups maps each movement pattern to the muscle
// groups it rolls up. MovementAll covers the entire catalog.
var MovementPatternMuscleGroups = map[MovementPattern][]MuscleGroup{
	MovementPush: {MuscleChest, MuscleShoulders, MuscleTriceps},
	MovementPull: {MuscleBack, MuscleBiceps, MuscleForearms},
	MovementLegs: {MuscleQuads, MuscleHamstrings, MuscleGlutes, MuscleCalves},
	MovementCore: {MuscleCore},
	MovementAll:  AllMuscleGroups(),
}

// Valid reports whether p is one of the five known movement patterns.
func (p MovementPattern) Valid() bool {
	switch p {
	case MovementPush, MovementPull, MovementLegs, MovementCore, MovementAll:
		return true
	}
	return false
}

// MuscleGroups returns the muscle groups behind this pattern. Returns
// nil for an unknown pattern.
func (p MovementPattern) MuscleGroups() []MuscleGroup {
	return MovementPatternMuscleGroups[p]
}
