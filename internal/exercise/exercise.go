package exercise

import "time"

// Exercise is a single canonical movement in the shared catalog
// (e.g., "Back Squat", "Push-up"). It's the catalog entry, not a
// logged set or a workout instance.
type Exercise struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	MuscleGroups []MuscleGroup `json:"muscle_groups"`
	Equipment    []Equipment   `json:"equipment"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	DeletedAt    *time.Time    `json:"-"`
}

// Validate checks that the exercise has all required fields and that
// all enum values are recognized. Returns the first error encountered.
func (e *Exercise) Validate() error {
	if e.Name == "" {
		return ErrNameRequired
	}
	if len(e.MuscleGroups) == 0 {
		return ErrMuscleGroupsRequired
	}
	for _, mg := range e.MuscleGroups {
		if !mg.Valid() {
			return &InvalidEnumError{Field: "muscle_group", Value: string(mg)}
		}
	}
	for _, eq := range e.Equipment {
		if !eq.Valid() {
			return &InvalidEnumError{Field: "equipment", Value: string(eq)}
		}
	}
	return nil
}
