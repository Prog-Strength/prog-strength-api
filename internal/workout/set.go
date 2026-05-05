package workout

import "github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"

// Set is a single set of an exercise: a number of reps performed
// against a given weight. Bodyweight exercises use Weight=0.
// WeightUnit lives in the user package but is stored alongside each
// set to preserve the user's original entry without conversion drift.
type Set struct {
	Reps   int             `json:"reps"`
	Weight float64         `json:"weight"`
	Unit   user.WeightUnit `json:"unit"`
}

func (s *Set) Validate() error {
	if s.Reps <= 0 {
		return ErrInvalidReps
	}
	if s.Weight < 0 {
		return ErrInvalidWeight
	}
	if !s.Unit.Valid() {
		return &InvalidEnumError{Field: "unit", Value: string(s.Unit)}
	}
	return nil
}
