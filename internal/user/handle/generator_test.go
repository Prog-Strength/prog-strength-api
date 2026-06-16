package handle

import (
	"errors"
	"strings"
	"testing"
)

// TestSlugifyHandle covers the display-name → handle transformation, asserting
// validity via a ValidateUsername round-trip rather than fragile exact strings
// where folding behavior is the point.
func TestSlugifyHandle(t *testing.T) {
	t.Run("simple_name", func(t *testing.T) {
		got := slugifyHandle("Sam Lifter")
		if got != "sam_lifter" {
			t.Fatalf("slugifyHandle(%q) = %q, want %q", "Sam Lifter", got, "sam_lifter")
		}
		if _, err := ValidateUsername(got); err != nil {
			t.Fatalf("slugifyHandle result %q failed ValidateUsername: %v", got, err)
		}
	})

	t.Run("accented_folds_to_valid", func(t *testing.T) {
		got := slugifyHandle("Café Crawl")
		if got == "" {
			t.Fatalf("slugifyHandle(%q) = empty, want non-empty", "Café Crawl")
		}
		if _, err := ValidateUsername(got); err != nil {
			t.Fatalf("slugifyHandle result %q failed ValidateUsername: %v", got, err)
		}
	})

	t.Run("empty_results", func(t *testing.T) {
		for _, in := range []string{"", "!!!", "42"} {
			if got := slugifyHandle(in); got != "" {
				t.Errorf("slugifyHandle(%q) = %q, want empty", in, got)
			}
		}
	})

	t.Run("reserved_is_unusable", func(t *testing.T) {
		if got := slugifyHandle("admin"); got != "" {
			t.Errorf("slugifyHandle(%q) = %q, want empty (reserved)", "admin", got)
		}
	})
}

// TestFallbackHandle confirms the id-derived fallback is always valid.
func TestFallbackHandle(t *testing.T) {
	got := fallbackHandle("3f2a9c10-dead-beef")
	if !strings.HasPrefix(got, "user_") {
		t.Errorf("fallbackHandle = %q, want user_ prefix", got)
	}
	if _, err := ValidateUsername(got); err != nil {
		t.Fatalf("fallbackHandle result %q failed ValidateUsername: %v", got, err)
	}
}

// alwaysFree / alwaysTaken are test helpers for the exists callback.
func alwaysFree(string) (bool, error)  { return false, nil }
func alwaysTaken(string) (bool, error) { return true, nil }

func TestGenerateHandle(t *testing.T) {
	t.Run("happy_path_uses_slug", func(t *testing.T) {
		got, err := GenerateHandle("Sam Lifter", "id-1", alwaysFree)
		if err != nil {
			t.Fatalf("GenerateHandle error: %v", err)
		}
		if got != "sam_lifter" {
			t.Errorf("GenerateHandle = %q, want %q", got, "sam_lifter")
		}
	})

	t.Run("collision_suffixing", func(t *testing.T) {
		taken := map[string]bool{"sam": true, "sam2": true}
		exists := func(h string) (bool, error) { return taken[h], nil }
		got, err := GenerateHandle("Sam", "id-1", exists)
		if err != nil {
			t.Fatalf("GenerateHandle error: %v", err)
		}
		if got != "sam3" {
			t.Errorf("GenerateHandle = %q, want %q", got, "sam3")
		}
	})

	t.Run("garbage_name_uses_fallback", func(t *testing.T) {
		got, err := GenerateHandle("!!!", "3f2a9c10-dead-beef", alwaysFree)
		if err != nil {
			t.Fatalf("GenerateHandle error: %v", err)
		}
		if !strings.HasPrefix(got, "user_") {
			t.Errorf("GenerateHandle = %q, want user_ prefix", got)
		}
		if _, err := ValidateUsername(got); err != nil {
			t.Fatalf("GenerateHandle result %q failed ValidateUsername: %v", got, err)
		}
	})

	t.Run("reserved_slug_never_returned", func(t *testing.T) {
		got, err := GenerateHandle("admin", "3f2a9c10-dead-beef", alwaysFree)
		if err != nil {
			t.Fatalf("GenerateHandle error: %v", err)
		}
		if got == "admin" {
			t.Errorf("GenerateHandle returned reserved %q", got)
		}
		if _, err := ValidateUsername(got); err != nil {
			t.Fatalf("GenerateHandle result %q failed ValidateUsername: %v", got, err)
		}
	})

	t.Run("exists_error_propagates", func(t *testing.T) {
		boom := errors.New("boom")
		exists := func(string) (bool, error) { return false, boom }
		if _, err := GenerateHandle("Sam Lifter", "id-1", exists); !errors.Is(err, boom) {
			t.Fatalf("GenerateHandle error = %v, want %v", err, boom)
		}
	})

	t.Run("exhaustion_returns_fallback", func(t *testing.T) {
		got, err := GenerateHandle("Sam", "3f2a9c10-dead-beef", alwaysTaken)
		if err != nil {
			t.Fatalf("GenerateHandle error: %v", err)
		}
		if got != fallbackHandle("3f2a9c10-dead-beef") {
			t.Errorf("GenerateHandle = %q, want fallback %q", got, fallbackHandle("3f2a9c10-dead-beef"))
		}
	})
}
