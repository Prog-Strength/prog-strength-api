package running

import (
	"errors"
	"testing"
)

// parseAndValidate mirrors what the handler will do: parse, mapping a
// parse failure to a SlugParseFailed ValidationError, then validate.
func parseAndValidate(data []byte) error {
	p, err := parseTCX(data)
	if err != nil {
		return validationErr(SlugParseFailed, err.Error())
	}
	return validate(p)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		wantSlug string // "" means expect success
	}{
		{"running 5k is valid", "typical_5k.tcx", ""},
		{"intervals is valid", "intervals.tcx", ""},
		{"biking is rejected", "biking.tcx", SlugNotRunning},
		{"zero-distance is empty", "empty.tcx", SlugEmpty},
		{"malformed is parse failure", "malformed.tcx", SlugParseFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseAndValidate(readFixture(t, tt.fixture))
			if tt.wantSlug == "" {
				if err != nil {
					t.Fatalf("got error %v, want success", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("got %v, want *ValidationError", err)
			}
			if ve.Slug != tt.wantSlug {
				t.Errorf("slug = %q, want %q", ve.Slug, tt.wantSlug)
			}
		})
	}
}
