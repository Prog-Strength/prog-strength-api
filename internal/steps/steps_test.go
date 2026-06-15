package steps

import (
	"errors"
	"testing"
)

func TestEntry_Validate(t *testing.T) {
	tests := []struct {
		name    string
		steps   int
		wantErr error
	}{
		{"zero is fine", 0, nil},
		{"normal", 8400, nil},
		{"max boundary", MaxSteps, nil},
		{"negative", -1, ErrStepsOutOfRange},
		{"over max", MaxSteps + 1, ErrStepsOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Entry{UserID: "u1", Date: "2026-06-14", Steps: tt.steps}
			err := e.Validate()
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
