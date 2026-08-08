package quotes

import (
	"strings"
	"testing"
)

// These tests guard the shipped corpus itself rather than the loader. They are
// the safety net for hand-editing data/*.json — including by an agent — so a
// bad append fails CI instead of quietly shipping a malformed quote.

func TestCorpus_LoadsAndIsNotTrivial(t *testing.T) {
	all := All()
	if len(all) < 2 {
		t.Fatalf("corpus has %d quotes; Pick needs a real pool to select from", len(all))
	}
}

func TestCorpus_TextIsPresentable(t *testing.T) {
	for _, q := range All() {
		switch {
		case strings.TrimSpace(q.Text) != q.Text:
			t.Errorf("%s: text has leading or trailing whitespace", q.ID)
		case strings.TrimSpace(q.Author) != q.Author:
			t.Errorf("%s: author has leading or trailing whitespace", q.ID)
		}
		// The UI supplies the quotation marks, so baked-in wrapping quotes
		// would render as doubled punctuation.
		if strings.HasPrefix(q.Text, `"`) || strings.HasSuffix(q.Text, `"`) ||
			strings.HasPrefix(q.Text, "“") || strings.HasSuffix(q.Text, "”") {
			t.Errorf("%s: text is wrapped in quotation marks; the UI adds them", q.ID)
		}
		// Markdown emphasis pasted in from a notes app.
		if strings.HasPrefix(q.Text, "*") || strings.HasPrefix(q.Author, "*") {
			t.Errorf("%s: text or author carries markdown formatting", q.ID)
		}
		if strings.HasPrefix(q.Author, "-") || strings.HasPrefix(q.Author, "–") {
			t.Errorf("%s: author %q keeps its leading dash; the UI adds it", q.ID, q.Author)
		}
	}
}

func TestCorpus_IDsAreSlugs(t *testing.T) {
	// Ids are permanent (they decide which day a quote lands on) and appear in
	// the API payload, so keep them boringly URL-safe.
	for _, q := range All() {
		for _, r := range q.ID {
			isLower := r >= 'a' && r <= 'z'
			isDigit := r >= '0' && r <= '9'
			if !isLower && !isDigit && r != '-' {
				t.Errorf("%s: id must be lowercase letters, digits, and hyphens", q.ID)
				break
			}
		}
	}
}
