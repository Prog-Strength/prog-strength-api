package steps

import (
	"errors"
	"testing"
)

func TestGoal_Validate(t *testing.T) {
	tests := []struct {
		name    string
		goal    int
		wantErr error
	}{
		{"zero", 0, ErrGoalOutOfRange},
		{"negative", -100, ErrGoalOutOfRange},
		{"over max", MaxGoal + 1, ErrGoalOutOfRange},
		{"valid", 10000, nil},
		{"max boundary", MaxGoal, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := Goal{UserID: "u1", Goal: tt.goal}
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
