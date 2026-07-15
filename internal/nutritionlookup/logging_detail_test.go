package nutritionlookup

import (
	"strings"
	"testing"
)

func TestCandidateSummaryLineIncludesSourceAndFlags(t *testing.T) {
	c := newCandidate(
		"Halo Top Cookies and Brownies Bits and Pieces",
		"Halo Top",
		"1 container",
		Macros{Calories: 500, ProteinG: 20, FatG: 8, CarbsG: 48},
		"fatsecret", "999",
	)
	c = scaled(c, 1)
	c.PlausibilityWarning = plausibilityWarning(c.PerServing)

	line := candidateSummaryLine(1, c)
	if !strings.Contains(line, "fatsecret/999") {
		t.Errorf("line = %q, want source/id", line)
	}
	if !strings.Contains(line, "500kcal/serving") {
		t.Errorf("line = %q, want calories", line)
	}
	if !strings.Contains(line, "plausibility_warning") {
		t.Errorf("line = %q, want plausibility flag", line)
	}
}

func TestCandidateSummaryLineTruncatesLongNames(t *testing.T) {
	longName := strings.Repeat("a", 80)
	c := newCandidate(longName, "", "1 serving", Macros{Calories: 100}, "usda", "1")
	line := candidateSummaryLine(1, c)
	if strings.Contains(line, longName) {
		t.Errorf("line should truncate long name: %q", line)
	}
	if !strings.Contains(line, "…") {
		t.Errorf("line = %q, want ellipsis truncation marker", line)
	}
}
