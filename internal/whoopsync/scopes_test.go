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
		},
		{
			// A grant wider than we require is not a problem: offline rides
			// along on every real consent and nothing else reads it here.
			name:    "superset",
			granted: "read:recovery read:cycles read:sleep read:profile offline read:workout",
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
			if !slices.Equal(got, tt.want) {
				t.Errorf("MissingScopes(%q) = %v, want %v", tt.granted, got, tt.want)
			}
		})
	}
}
