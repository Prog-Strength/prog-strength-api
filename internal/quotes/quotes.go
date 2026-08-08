// Package quotes serves the daily quote behind the dashboard's quote tile.
//
// The corpus is static content compiled into the binary: every *.json file
// under data/ is embedded and flattened into one slice at load. Adding quotes
// means appending to a JSON array; adding a category means dropping a new file
// into data/. Neither touches this file — that is the whole point of the glob.
//
// There is no repository, no network, and no error path here: the data cannot
// fail to arrive at runtime because it is part of the executable. A malformed
// or duplicated entry fails at load (and, long before that, in the tests)
// rather than silently shrinking the pool.
package quotes

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"regexp"
	"sort"
)

//go:embed data/*.json
var dataFS embed.FS

// wikipediaArticle matches an English Wikipedia article URL and nothing else.
// The corpus is hand-edited, and the near misses are the ones worth catching:
// a copied `en.m.` mobile URL, a stale `http://`, or a bare `/wiki/` with the
// title lost. Anything off en.wikipedia.org is rejected outright rather than
// waved through, because the client styles these links on the assumption that
// every one of them goes to the same place.
var wikipediaArticle = regexp.MustCompile(`^https://en\.wikipedia\.org/wiki/[^\s]+$`)

// Quote is one entry in the corpus.
//
// Source and Tags are optional. Source records provenance — leave it empty for
// an attribution you have not actually verified, which is most circulating
// quotes. Tags is unused by today's selection; it exists so that picking from a
// themed pool later (a back-off line during a deload week, say) is a change to
// Pick rather than a reshaping of every data file.
//
// AuthorURL and SourceURL are the "learn more" links the tile hangs off the
// attribution. Both are optional and both are English Wikipedia article URLs —
// standardizing on one encyclopedia is what lets the client style every link
// identically instead of branching on the destination.
//
// AuthorURL points at the person, not at a claim that the person said this
// line: several attributions in the corpus are the popular one rather than a
// checked one (see data/README.md), and those quotes still link the human
// being. SourceURL is different — it can only appear alongside a Source, which
// is already a verified-only field.
type Quote struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Author    string   `json:"author"`
	AuthorURL string   `json:"author_url,omitempty"`
	Source    string   `json:"source,omitempty"`
	SourceURL string   `json:"source_url,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// corpus is the loaded, id-sorted set of every embedded quote.
var corpus = mustLoad()

// All returns the whole corpus, ordered by id. The slice is a copy so a caller
// cannot mutate the package's own state.
func All() []Quote {
	out := make([]Quote, len(corpus))
	copy(out, corpus)
	return out
}

// Pick returns the day's quote for one user — PickAt at offset 0.
//
// Selection is arbitrary but deterministic: the same (userID, localDate) always
// yields the same quote. That is what makes it a *daily* quote rather than a
// random one — it holds still across refreshes and across devices, and it turns
// over at the user's own local midnight instead of UTC's. Hashing the user in
// as well keeps every user off the same quote on the same day.
//
// localDate is a local calendar date in YYYY-MM-DD form. Resolving the timezone
// is the caller's job (see internal/daterange); this package never does it.
func Pick(userID, localDate string) Quote {
	return PickAt(userID, localDate, 0)
}

// PickAt returns the quote offset places past the day's quote, wrapping around
// the corpus. It backs the tile's reroll button: the caller advances a stored
// offset, so successive taps walk distinct quotes and only repeat after the
// whole corpus has been seen. Rerolling by re-randomizing instead would let the
// same quote come up twice in a row, which reads as a broken button.
//
// Which offset a user is on is persisted per local day by the dashboard (see
// migration 054), so a reroll survives a page load. This package stays unaware
// of that: it is handed an offset and returns a quote.
//
// offset may be negative or arbitrarily large; it is normalized into range.
func PickAt(userID, localDate string, offset int) Quote {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID))
	// Separator: without it ("ab","c") and ("a","bc") would hash identically.
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(localDate))

	n := len(corpus)
	base := int(h.Sum64() % uint64(n))
	// Go's % keeps the sign of the dividend, so a negative offset needs the
	// extra +n before the second fold to land back in [0,n).
	return corpus[((base+offset)%n+n)%n]
}

// mustLoad panics on an unusable corpus. The data is embedded at build time, so
// a failure here is deterministic and environment-independent: it cannot appear
// first in production, because any test run reaches this same code path.
func mustLoad() []Quote {
	qs, err := load(dataFS)
	if err != nil {
		panic("quotes: " + err.Error())
	}
	return qs
}

// load reads every data/*.json file, flattens them into one slice, and
// validates the result. It takes an fs.FS rather than reading the package
// variable directly so the tests can drive it with fixture filesystems —
// including deliberately broken ones — without tripping mustLoad's panic.
func load(fsys fs.FS) ([]Quote, error) {
	files, err := fs.Glob(fsys, "data/*.json")
	if err != nil {
		return nil, fmt.Errorf("glob data/*.json: %w", err)
	}
	// Glob is already lexical, but sorting states the dependency rather than
	// relying on it: load order must never affect the outcome.
	sort.Strings(files)

	var all []Quote
	ids := map[string]string{}     // id -> file that first claimed it
	texts := map[string]string{}   // text -> id that first used it
	authors := map[string]string{} // author -> the url the first linked quote gave them

	for _, name := range files {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var batch []Quote
		dec := json.NewDecoder(bytes.NewReader(b))
		// A hand-edited corpus makes key typos likely, and an unknown key is
		// otherwise dropped in silence. That matters most for the optional
		// fields ("tag" for "tags"): a typo there passes every check below and
		// just quietly loses the data.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&batch); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for i, q := range batch {
			switch {
			case q.ID == "":
				return nil, fmt.Errorf("%s[%d]: id is required", name, i)
			case q.Text == "":
				return nil, fmt.Errorf("%s: quote %q has no text", name, q.ID)
			case q.Author == "":
				return nil, fmt.Errorf("%s: quote %q has no author", name, q.ID)
			}
			if prev, dup := ids[q.ID]; dup {
				return nil, fmt.Errorf("%s: duplicate id %q (already in %s)", name, q.ID, prev)
			}
			if prev, dup := texts[q.Text]; dup {
				return nil, fmt.Errorf("%s: quote %q repeats the text of %q", name, q.ID, prev)
			}
			if q.AuthorURL != "" && !wikipediaArticle.MatchString(q.AuthorURL) {
				return nil, fmt.Errorf("%s: quote %q has author_url %q, want an https://en.wikipedia.org/wiki/ article", name, q.ID, q.AuthorURL)
			}
			// A source link with nothing to hang it on: the tile renders the
			// link *on* the source text, so this would be invisible.
			if q.SourceURL != "" && q.Source == "" {
				return nil, fmt.Errorf("%s: quote %q has a source_url without a source", name, q.ID)
			}
			if q.SourceURL != "" && !wikipediaArticle.MatchString(q.SourceURL) {
				return nil, fmt.Errorf("%s: quote %q has source_url %q, want an https://en.wikipedia.org/wiki/ article", name, q.ID, q.SourceURL)
			}
			// Storing the link per quote means one author's link is written out
			// once per quote they have. This is what keeps those copies honest:
			// the corpus may not claim two different people behind one name.
			// Quotes that omit the link are skipped — author_url is optional.
			if q.AuthorURL != "" {
				if prev, seen := authors[q.Author]; seen && prev != q.AuthorURL {
					return nil, fmt.Errorf("%s: quote %q links author %q to %q, but another quote already links to %q", name, q.ID, q.Author, q.AuthorURL, prev)
				}
				authors[q.Author] = q.AuthorURL
			}
			ids[q.ID] = name
			texts[q.Text] = q.ID
		}
		all = append(all, batch...)
	}

	if len(all) == 0 {
		return nil, errors.New("corpus is empty")
	}
	// Sorting by id makes Pick independent of file layout: splitting data/ into
	// more files, or renaming one, must not change which quote a user sees.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}
