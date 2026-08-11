package dashboard

import (
	"strings"
	"testing"
)

// Normalize is the read path's repair pass. It must be TOTAL — it never
// rejects — because a layout accepted under an older rule set can never be
// allowed to blank a dashboard. These cases pin each repair.
func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []Section
		want []Section
	}{
		{
			name: "empty layout gets the one-untitled-section floor",
			in:   nil,
			want: []Section{{ID: "s1", TileIDs: []TileID{}}},
		},
		{
			name: "valid layout passes through unchanged",
			in: []Section{
				{ID: "a", Title: "Endurance", TileIDs: []TileID{TileRunning, TileSteps}},
				{ID: "b", Title: "Strength", Collapsed: true, TileIDs: []TileID{TileLifting}},
			},
			want: []Section{
				{ID: "a", Title: "Endurance", TileIDs: []TileID{TileRunning, TileSteps}},
				{ID: "b", Title: "Strength", Collapsed: true, TileIDs: []TileID{TileLifting}},
			},
		},
		{
			name: "unknown tile ids are dropped, order preserved",
			in:   []Section{{ID: "a", TileIDs: []TileID{TileRunning, "retired_tile", TileSteps}}},
			want: []Section{{ID: "a", TileIDs: []TileID{TileRunning, TileSteps}}},
		},
		{
			name: "a tile repeated across sections is kept at its first occurrence",
			in: []Section{
				{ID: "a", TileIDs: []TileID{TileRunning, TileSteps}},
				{ID: "b", TileIDs: []TileID{TileRunning, TileLifting}},
			},
			want: []Section{
				{ID: "a", TileIDs: []TileID{TileRunning, TileSteps}},
				{ID: "b", TileIDs: []TileID{TileLifting}},
			},
		},
		{
			name: "titles are trimmed",
			in:   []Section{{ID: "a", Title: "  Recovery \n", TileIDs: []TileID{}}},
			want: []Section{{ID: "a", Title: "Recovery", TileIDs: []TileID{}}},
		},
		{
			name: "an empty section id gets a positional fallback",
			in:   []Section{{ID: "", TileIDs: []TileID{TileRunning}}},
			want: []Section{{ID: "s1", TileIDs: []TileID{TileRunning}}},
		},
		{
			name: "a duplicated section id is made unique",
			in: []Section{
				{ID: "dup", TileIDs: []TileID{TileRunning}},
				{ID: "dup", TileIDs: []TileID{TileSteps}},
			},
			want: []Section{
				{ID: "dup", TileIDs: []TileID{TileRunning}},
				{ID: "s2", TileIDs: []TileID{TileSteps}},
			},
		},
		{
			name: "a retired tile becomes its replacement, in its own slot",
			in:   []Section{{ID: "a", TileIDs: []TileID{TileSteps, TileRecoveryTrend, TileLifting}}},
			want: []Section{{ID: "a", TileIDs: []TileID{TileSteps, TileHRVBalance, TileLifting}}},
		},
		{
			name: "a layout holding both the retired tile and its replacement keeps one",
			in: []Section{
				{ID: "a", TileIDs: []TileID{TileRecoveryTrend}},
				{ID: "b", TileIDs: []TileID{TileHRVBalance, TileSteps}},
			},
			want: []Section{
				{ID: "a", TileIDs: []TileID{TileHRVBalance}},
				{ID: "b", TileIDs: []TileID{TileSteps}},
			},
		},
		{
			name: "the same pair the other way round also keeps one",
			in: []Section{
				{ID: "a", TileIDs: []TileID{TileHRVBalance}},
				{ID: "b", TileIDs: []TileID{TileRecoveryTrend, TileSteps}},
			},
			want: []Section{
				{ID: "a", TileIDs: []TileID{TileHRVBalance}},
				{ID: "b", TileIDs: []TileID{TileSteps}},
			},
		},
		{
			name: "a section emptied by filtering survives as an empty section",
			in:   []Section{{ID: "a", Title: "Gone", TileIDs: []TileID{"retired_tile"}}},
			want: []Section{{ID: "a", Title: "Gone", TileIDs: []TileID{}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertSections(t, Normalize(tc.in).Sections, tc.want)
		})
	}
}

// A positional fallback id can itself collide with a real id later in the list.
// Normalize must keep extending until it is unique rather than emitting a
// duplicate — duplicate ids break the web surface's list keys and drag targets.
func TestNormalize_FallbackIDAvoidsCollision(t *testing.T) {
	t.Parallel()

	got := Normalize([]Section{
		{ID: "", TileIDs: []TileID{TileRunning}}, // wants the fallback "s1"
		{ID: "s1", TileIDs: []TileID{TileSteps}}, // already holds "s1"
		{ID: "", TileIDs: []TileID{TileLifting}}, // wants "s3"
	}).Sections

	seen := map[string]bool{}
	for i, s := range got {
		if s.ID == "" {
			t.Errorf("section %d has an empty id", i)
		}
		if seen[s.ID] {
			t.Errorf("section %d repeats id %q", i, s.ID)
		}
		seen[s.ID] = true
	}
}

func TestNormalize_TruncatesOverlongTitle(t *testing.T) {
	t.Parallel()

	got := Normalize([]Section{{ID: "a", Title: strings.Repeat("x", MaxSectionTitleLen+10)}}).Sections
	if n := len([]rune(got[0].Title)); n != MaxSectionTitleLen {
		t.Errorf("title length = %d, want %d", n, MaxSectionTitleLen)
	}
}

// Truncation counts RUNES, not bytes: a title of multi-byte characters at the
// cap must survive whole rather than being cut mid-character.
func TestNormalize_TitleCapCountsRunes(t *testing.T) {
	t.Parallel()

	title := strings.Repeat("é", MaxSectionTitleLen)
	got := Normalize([]Section{{ID: "a", Title: title}}).Sections
	if got[0].Title != title {
		t.Errorf("title = %q, want it unchanged at exactly the rune cap", got[0].Title)
	}
}

func TestNormalize_DropsSectionsPastTheCap(t *testing.T) {
	t.Parallel()

	in := make([]Section, MaxSections+5)
	for i := range in {
		in[i] = Section{ID: "s" + string(rune('A'+i))}
	}
	if got := Normalize(in).Sections; len(got) != MaxSections {
		t.Errorf("sections = %d, want %d", len(got), MaxSections)
	}
}

// TileIDs flattens in display order across sections — the set the summary read
// path builds from.
func TestLayout_TileIDsFlattens(t *testing.T) {
	t.Parallel()

	l := Layout{Sections: []Section{
		{ID: "a", TileIDs: []TileID{TileRunning, TileSteps}},
		{ID: "b", TileIDs: []TileID{}},
		{ID: "c", TileIDs: []TileID{TileLifting}},
	}}
	assertTileIDs(t, l.TileIDs(), []TileID{TileRunning, TileSteps, TileLifting})
}

func TestSingleSection(t *testing.T) {
	t.Parallel()

	assertSections(t, SingleSection([]TileID{TileRunning}),
		[]Section{{ID: "s1", TileIDs: []TileID{TileRunning}}})
	// A nil tile list must still yield a non-nil empty slice so the JSON blob
	// carries [] rather than null.
	assertSections(t, SingleSection(nil), []Section{{ID: "s1", TileIDs: []TileID{}}})
}
