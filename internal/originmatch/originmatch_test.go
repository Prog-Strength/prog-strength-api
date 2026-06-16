package originmatch

import "testing"

func TestAllowReturnTo(t *testing.T) {
	// A Vercel-style preview pattern scoped to project + scope, plus a
	// prod origin and the mobile app's custom scheme (empty host).
	allowed := []string{
		"https://progstrength.fitness",
		"https://prog-strength-web-*-jimmy-wallaces-projects.vercel.app",
		"http://localhost:3000",
		"progstrength://",
	}

	tests := []struct {
		name     string
		returnTo string
		want     bool
	}{
		{
			name:     "exact prod origin",
			returnTo: "https://progstrength.fitness/calendar",
			want:     true,
		},
		{
			name:     "branch preview matches wildcard (the bug being fixed)",
			returnTo: "https://prog-strength-web-git-dx-calendar-view-jimmy-wallaces-projects.vercel.app/login",
			want:     true,
		},
		{
			name:     "different branch preview also matches wildcard",
			returnTo: "https://prog-strength-web-git-feat-foo-abc123-jimmy-wallaces-projects.vercel.app/x",
			want:     true,
		},
		{
			name:     "localhost dev",
			returnTo: "http://localhost:3000/auth/callback",
			want:     true,
		},
		{
			name:     "mobile custom scheme, exact match, empty host",
			returnTo: "progstrength:///auth/callback",
			want:     true,
		},
		{
			name:     "wrong scheme on a preview host is rejected",
			returnTo: "http://prog-strength-web-git-x-jimmy-wallaces-projects.vercel.app/",
			want:     false,
		},
		{
			name:     "foreign vercel scope is rejected (not our project scope)",
			returnTo: "https://prog-strength-web-git-x-attacker-projects.vercel.app/",
			want:     false,
		},
		{
			name:     "bare vercel apex is rejected",
			returnTo: "https://attacker.vercel.app/",
			want:     false,
		},
		{
			name:     "unrelated origin is rejected",
			returnTo: "https://evil.example.com/",
			want:     false,
		},
		{
			name:     "custom scheme cannot smuggle a host past the exact entry",
			returnTo: "progstrength://evil.example.com/auth",
			want:     false,
		},
		{
			name:     "empty return_to is rejected",
			returnTo: "",
			want:     false,
		},
		{
			name:     "schemeless value is rejected",
			returnTo: "//prog-strength-web-git-x-jimmy-wallaces-projects.vercel.app",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowReturnTo(tc.returnTo, allowed); got != tc.want {
				t.Errorf("AllowReturnTo(%q) = %v, want %v", tc.returnTo, got, tc.want)
			}
		})
	}
}

func TestAllowReturnTo_EmptyAllowlist(t *testing.T) {
	// With the feature disabled (no configured origins), nothing matches.
	if AllowReturnTo("https://progstrength.fitness/", nil) {
		t.Error("expected no match against an empty allowlist")
	}
}

func TestMatchOrigin_WildcardCannotOverlap(t *testing.T) {
	// A prefix+suffix longer than the candidate must not match (mirrors
	// go-chi/cors): the wildcard segment cannot have negative length.
	patterns := []string{"https://prog-strength-web-*-scope.vercel.app"}
	if MatchOrigin("https://scope.vercel.app", patterns) {
		t.Error("prefix and suffix overlapped on a too-short origin")
	}
}
