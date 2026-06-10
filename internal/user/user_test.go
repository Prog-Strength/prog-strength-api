package user

import (
	"errors"
	"strings"
	"testing"
)

// floatPtr is a small helper for building *float64 height values in tests.
func floatPtr(v float64) *float64 { return &v }

func TestValidate_DisplayName(t *testing.T) {
	base := func(name string) *User {
		return &User{
			Email:        "lifter@example.com",
			DisplayName:  name,
			WeightUnit:   WeightUnitPounds,
			DistanceUnit: DistanceUnitMiles,
		}
	}

	tests := []struct {
		name    string
		display string
		wantErr error
	}{
		{"empty rejected", "", ErrDisplayNameRequired},
		{"61 runes rejected", strings.Repeat("a", DisplayNameMaxLen+1), ErrDisplayNameTooLong},
		{"60 runes accepted", strings.Repeat("a", DisplayNameMaxLen), nil},
		// Rune-counting (not byte-counting): 60 multibyte chars is fine even
		// though it's well over 60 bytes; 61 multibyte chars is rejected.
		{"60 multibyte runes accepted", strings.Repeat("é", DisplayNameMaxLen), nil},
		{"61 multibyte runes rejected", strings.Repeat("é", DisplayNameMaxLen+1), ErrDisplayNameTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := base(tt.display).Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_HeightCm(t *testing.T) {
	base := func(h *float64) *User {
		return &User{
			Email:        "lifter@example.com",
			DisplayName:  "Lifter",
			WeightUnit:   WeightUnitPounds,
			DistanceUnit: DistanceUnitMiles,
			HeightCm:     h,
		}
	}

	tests := []struct {
		name    string
		height  *float64
		wantErr error
	}{
		{"nil accepted", nil, nil},
		{"min accepted", floatPtr(HeightCmMin), nil},
		{"max accepted", floatPtr(HeightCmMax), nil},
		{"below min rejected", floatPtr(49.9), ErrHeightOutOfRange},
		{"above max rejected", floatPtr(250.1), ErrHeightOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := base(tt.height).Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
