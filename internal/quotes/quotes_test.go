package quotes

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestPick_StableForTheSameUserAndDay(t *testing.T) {
	// The whole premise of a "daily" quote: refreshing the dashboard, or
	// opening it on a second device, must not reshuffle it.
	first := Pick("user-1", "2026-08-08")
	for i := 0; i < 50; i++ {
		if got := Pick("user-1", "2026-08-08"); got.ID != first.ID {
			t.Fatalf("Pick is not stable: call %d gave %q, want %q", i, got.ID, first.ID)
		}
	}
}

func TestPick_VariesAcrossDaysAndUsers(t *testing.T) {
	// Not a strict guarantee for any single pair — a 12-quote corpus collides
	// often — so assert on the spread instead: a run of days must not be one
	// quote over and over, and a set of users must not share one quote.
	days := map[string]bool{}
	for _, d := range []string{
		"2026-08-01", "2026-08-02", "2026-08-03", "2026-08-04", "2026-08-05",
		"2026-08-06", "2026-08-07", "2026-08-08", "2026-08-09", "2026-08-10",
	} {
		days[Pick("user-1", d).ID] = true
	}
	if len(days) < 3 {
		t.Errorf("10 consecutive days produced only %d distinct quotes, want >= 3", len(days))
	}

	users := map[string]bool{}
	for _, u := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		users[Pick(u, "2026-08-08").ID] = true
	}
	if len(users) < 3 {
		t.Errorf("10 users on one day produced only %d distinct quotes, want >= 3", len(users))
	}
}

func TestPickAt_ZeroOffsetMatchesPick(t *testing.T) {
	if got, want := PickAt("user-1", "2026-08-08", 0), Pick("user-1", "2026-08-08"); got.ID != want.ID {
		t.Errorf("PickAt(...,0) = %q, want Pick(...) = %q", got.ID, want.ID)
	}
}

func TestPickAt_WalksTheWholeCorpusWithoutRepeating(t *testing.T) {
	// The reroll contract: n taps yield n distinct quotes, and tap n+1 wraps
	// back to the day's quote.
	n := len(All())
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		q := PickAt("user-1", "2026-08-08", i)
		if seen[q.ID] {
			t.Fatalf("offset %d repeated quote %q before the corpus was exhausted", i, q.ID)
		}
		seen[q.ID] = true
	}
	if len(seen) != n {
		t.Errorf("walked %d offsets but saw %d distinct quotes", n, len(seen))
	}
	if got, want := PickAt("user-1", "2026-08-08", n), Pick("user-1", "2026-08-08"); got.ID != want.ID {
		t.Errorf("offset %d = %q, want wrap to %q", n, got.ID, want.ID)
	}
}

func TestPickAt_NormalizesOutOfRangeOffsets(t *testing.T) {
	n := len(All())
	want := Pick("user-1", "2026-08-08").ID

	// A negative offset must not panic (Go's % keeps the dividend's sign) and
	// must land on a real quote; -n specifically wraps to the day's quote.
	if got := PickAt("user-1", "2026-08-08", -n); got.ID != want {
		t.Errorf("offset -%d = %q, want %q", n, got.ID, want)
	}
	if got := PickAt("user-1", "2026-08-08", -1); got.ID == "" {
		t.Error("offset -1 returned the zero Quote")
	}
	if got := PickAt("user-1", "2026-08-08", n*1000); got.ID != want {
		t.Errorf("offset %d = %q, want %q", n*1000, got.ID, want)
	}
}

func TestAll_IsACopy(t *testing.T) {
	got := All()
	if len(got) == 0 {
		t.Fatal("All() is empty")
	}
	got[0].Text = "mutated"
	if All()[0].Text == "mutated" {
		t.Error("mutating the All() result changed the package corpus")
	}
}

func TestAll_SortedByID(t *testing.T) {
	// Pick indexes into this order, so it must not depend on file layout.
	all := All()
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Errorf("corpus not sorted by id: %q precedes %q", all[i-1].ID, all[i].ID)
		}
	}
}

// --- load() failure modes, driven by fixture filesystems ---

func TestLoad_RejectsBadCorpora(t *testing.T) {
	valid := `[{"id":"a","text":"A","author":"X"}]`

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "empty corpus",
			files: map[string]string{"data/empty.json": `[]`},
			want:  "corpus is empty",
		},
		{
			name:  "malformed json",
			files: map[string]string{"data/bad.json": `[{"id":`},
			want:  "parse data/bad.json",
		},
		{
			name:  "unknown field",
			files: map[string]string{"data/typo.json": `[{"id":"a","text":"A","author":"X","tag":["y"]}]`},
			want:  "parse data/typo.json",
		},
		{
			name:  "missing id",
			files: map[string]string{"data/x.json": `[{"text":"A","author":"X"}]`},
			want:  "id is required",
		},
		{
			name:  "missing text",
			files: map[string]string{"data/x.json": `[{"id":"a","author":"X"}]`},
			want:  "has no text",
		},
		{
			name:  "missing author",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A"}]`},
			want:  "has no author",
		},
		{
			name: "duplicate id across files",
			files: map[string]string{
				"data/one.json": valid,
				"data/two.json": `[{"id":"a","text":"different","author":"Y"}]`,
			},
			want: "duplicate id",
		},
		{
			name: "duplicate text across files",
			files: map[string]string{
				"data/one.json": valid,
				"data/two.json": `[{"id":"b","text":"A","author":"Y"}]`,
			},
			want: "repeats the text",
		},
		{
			name:  "author_url off wikipedia",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","author_url":"https://example.com/X"}]`},
			want:  "author_url",
		},
		{
			name:  "author_url on the mobile host",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","author_url":"https://en.m.wikipedia.org/wiki/X"}]`},
			want:  "author_url",
		},
		{
			name:  "author_url over plain http",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","author_url":"http://en.wikipedia.org/wiki/X"}]`},
			want:  "author_url",
		},
		{
			name:  "author_url with no article path",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","author_url":"https://en.wikipedia.org/wiki/"}]`},
			want:  "author_url",
		},
		{
			name:  "source_url without a source",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","source_url":"https://en.wikipedia.org/wiki/W"}]`},
			want:  "source_url without a source",
		},
		{
			name:  "source_url off wikipedia",
			files: map[string]string{"data/x.json": `[{"id":"a","text":"A","author":"X","source":"W","source_url":"https://example.com/W"}]`},
			want:  "source_url",
		},
		{
			name: "one author with conflicting urls",
			files: map[string]string{
				"data/one.json": `[{"id":"a","text":"A","author":"X","author_url":"https://en.wikipedia.org/wiki/X"}]`,
				"data/two.json": `[{"id":"b","text":"B","author":"X","author_url":"https://en.wikipedia.org/wiki/Someone_Else"}]`,
			},
			want: "already links to",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, body := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			_, err := load(fsys)
			if err == nil {
				t.Fatalf("load succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("load error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoad_AcceptsOneAuthorLinkedConsistently(t *testing.T) {
	// The flip side of the conflicting-url check: the same author across many
	// quotes is the normal case, and repeating the identical link is how the
	// per-quote shape is meant to be used. Only a *disagreement* is an error.
	// A quote that omits the link entirely is fine too — author_url is optional,
	// because not every author has an article.
	fsys := fstest.MapFS{
		"data/one.json": &fstest.MapFile{Data: []byte(
			`[{"id":"a","text":"A","author":"X","author_url":"https://en.wikipedia.org/wiki/X"}]`)},
		"data/two.json": &fstest.MapFile{Data: []byte(
			`[{"id":"b","text":"B","author":"X","author_url":"https://en.wikipedia.org/wiki/X"},` +
				`{"id":"c","text":"C","author":"X"}]`)},
	}
	if _, err := load(fsys); err != nil {
		t.Errorf("load: %v", err)
	}
}

func TestLoad_FlattensMultipleFiles(t *testing.T) {
	// The extensibility contract: a new data/*.json file joins the pool with no
	// code change, and load order never affects the result.
	fsys := fstest.MapFS{
		"data/zebra.json": &fstest.MapFile{Data: []byte(`[{"id":"z","text":"Z","author":"X"}]`)},
		"data/alpha.json": &fstest.MapFile{Data: []byte(`[{"id":"a","text":"A","author":"X"}]`)},
	}
	got, err := load(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d quotes from 2 files, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "z" {
		t.Errorf("got order [%q %q], want [a z] regardless of filename order", got[0].ID, got[1].ID)
	}
}
