package bodyweight

import (
	"errors"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

func TestGoal_Validate(t *testing.T) {
	tests := []struct {
		name    string
		weight  float64
		unit    user.WeightUnit
		wantErr error
	}{
		{"zero weight", 0, user.WeightUnitPounds, ErrWeightNonPositive},
		{"negative weight", -10, user.WeightUnitPounds, ErrWeightNonPositive},
		{"weight too large", 2001, user.WeightUnitPounds, ErrWeightTooLarge},
		{"empty unit", 175, "", ErrInvalidUnit},
		{"unknown unit", 175, "stone", ErrInvalidUnit},
		{"valid pounds", 175, user.WeightUnitPounds, nil},
		{"valid kilograms", 80, user.WeightUnitKilograms, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := Goal{Weight: tt.weight, Unit: tt.unit}
			err := g.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("want %v, got %v", tt.wantErr, err)
			}
		})
	}
}
