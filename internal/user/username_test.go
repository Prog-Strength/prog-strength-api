package user

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateUsername_Valid covers handles that should pass and confirms the
// returned value is the canonical (lowercased, @-stripped) form.
func TestValidateUsername_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"simple", "jimlifts", "jimlifts"},
		{"with_digits", "jim2lifts", "jim2lifts"},
		{"with_underscore", "jim_lifts", "jim_lifts"},
		{"uppercase_folds", "JimLifts", "jimlifts"},
		{"at_prefix_stripped", "@JimLifts", "jimlifts"},
		{"min_length", "abc", "abc"},
		{"max_length", strings.Repeat("a", 30), strings.Repeat("a", 30)},
		{"surrounding_space_trimmed", "  jimlifts  ", "jimlifts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateUsername(tc.raw)
			if err != nil {
				t.Fatalf("ValidateUsername(%q) error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ValidateUsername(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestValidateUsername_Invalid covers charset/length/shape rejections.
func TestValidateUsername_Invalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"too_short", "ab"},
		{"too_long", strings.Repeat("a", 31)},
		{"leading_digit", "1jim"},
		{"leading_underscore", "_jim"},
		{"hyphen", "jim-lifts"},
		{"dot", "jim.lifts"},
		{"space_inside", "jim lifts"},
		{"empty", ""},
		{"only_at", "@"},
		{"unicode", "jimléfts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateUsername(tc.raw)
			if !errors.Is(err, ErrUsernameInvalid) {
				t.Fatalf("ValidateUsername(%q) error = %v, want ErrUsernameInvalid", tc.raw, err)
			}
		})
	}
}

// TestValidateUsername_Reserved confirms denylisted names are rejected after
// passing charset validation, with the reserved-specific error.
func TestValidateUsername_Reserved(t *testing.T) {
	// Note: short reserved names like "me" are blocked by the length check
	// before the denylist is consulted (covered by the invalid-length cases);
	// these are all >=3 chars so they exercise the reserved path itself.
	for _, raw := range []string{"admin", "API", "@settings", "ProgStrength", "followers"} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateUsername(raw)
			if !errors.Is(err, ErrUsernameReserved) {
				t.Fatalf("ValidateUsername(%q) error = %v, want ErrUsernameReserved", raw, err)
			}
		})
	}
}
