package exercise

// Catalog is the canonical list of supported exercises.
// This is the source of truth until exercises are persisted in a database.
//
// To add an exercise: append to this slice. IDs are stable, human-readable
// slugs — never renumber or rename them, because workout logs will reference
// them. To remove an exercise, set DeletedAt rather than deleting the entry.
var Catalog = []Exercise{
	{
		ID:           "leg-extension",
		Name:         "Leg Extension",
		Description:  "Seated on a machine, extend the knees against a padded lever to isolate the quadriceps.",
		MuscleGroups: []MuscleGroup{MuscleQuads},
		Equipment:    []Equipment{EquipmentMachine},
	},
	{
		ID:           "machine-lying-leg-curl",
		Name:         "Machine Lying Leg Curl",
		Description:  "Lying face-down on a machine, curl the heels toward the glutes to isolate the hamstrings.",
		MuscleGroups: []MuscleGroup{MuscleHamstrings},
		Equipment:    []Equipment{EquipmentMachine},
	},
	{
		ID:           "barbell-high-bar-back-squat",
		Name:         "Barbell High Bar Back Squat",
		Description:  "Barbell held high on the traps; squat to depth with a more upright torso than the low-bar variant.",
		MuscleGroups: []MuscleGroup{MuscleQuads, MuscleGlutes, MuscleHamstrings, MuscleCore},
		Equipment:    []Equipment{EquipmentBarbell, EquipmentRack},
	},
	{
		ID:           "barbell-calf-raise",
		Name:         "Barbell Calf Raise",
		Description:  "Standing with a barbell across the upper back, raise onto the balls of the feet to target the calves.",
		MuscleGroups: []MuscleGroup{MuscleCalves},
		Equipment:    []Equipment{EquipmentBarbell},
	},
	{
		ID:           "bodyweight-squat",
		Name:         "Bodyweight Squat",
		Description:  "Squat to depth using only bodyweight; foundational movement for lower body mobility and conditioning.",
		MuscleGroups: []MuscleGroup{MuscleQuads, MuscleGlutes, MuscleHamstrings},
		Equipment:    []Equipment{EquipmentNone},
	},
	{
		ID:           "hanging-leg-raise",
		Name:         "Hanging Leg Raise",
		Description:  "Hanging from a pull-up bar, raise the legs to engage the lower abdominals and hip flexors.",
		MuscleGroups: []MuscleGroup{MuscleCore},
		Equipment:    []Equipment{EquipmentPullupBar},
	},
	{
		ID:           "decline-bench-sit-up",
		Name:         "Decline Bench Sit Up",
		Description:  "Anchored on a decline bench, perform sit-ups against gravity to target the abdominals.",
		MuscleGroups: []MuscleGroup{MuscleCore},
		Equipment:    []Equipment{EquipmentDeclineBench},
	},
}
