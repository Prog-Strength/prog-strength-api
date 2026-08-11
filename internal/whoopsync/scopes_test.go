package whoopsync

import (
	"slices"
	"testing"
)

func TestMissingScopes(t *testing.T) {
	tests := []struct {
		name    string
		granted string
		want    []string
	}{
		{
			name:    "exact match",
			granted: "read:recovery read:cycles read:sleep read:profile",
			want:    []string{},
		},
		{
			// A grant wider than we require is not a problem: offline rides
			// along on every real consent and nothing else reads it here.
			name:    "superset",
			granted: "read:recovery read:cycles read:sleep read:profile offline read:workout",
			want:    []string{},
		},
		{
			name:    "empty granted string",
			granted: "",
			want:    []string{"read:recovery", "read:cycles", "read:sleep", "read:profile"},
		},
		{
			// WHOOP echoes the granted scopes back in its own order, which is
			// not ours.
			name:    "different order",
			granted: "read:profile offline read:cycles read:recovery read:sleep",
			want:    []string{},
		},
		{
			// The state every pre-sleep connection is in until it re-consents.
			name:    "missing read:sleep only",
			granted: "read:recovery read:cycles read:profile offline",
			want:    []string{"read:sleep"},
		},
		{
			name:    "result follows RequiredScopes order not granted order",
			granted: "read:cycles",
			want:    []string{"read:recovery", "read:sleep", "read:profile"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MissingScopes(tt.granted)
			// Non-nil is asserted separately from the contents: slices.Equal
			// treats nil and empty as equal, and the nil-vs-empty distinction is
			// exactly what the JSON surfaces depend on.
			if got == nil {
				t.Fatalf("MissingScopes(%q) = nil, want a non-nil slice", tt.granted)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("MissingScopes(%q) = %v, want %v", tt.granted, got, tt.want)
			}
		})
	}
}
