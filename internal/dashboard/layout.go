package dashboard

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ErrLayoutNotFound is returned by Repository.Get when the user has no stored
// layout row (they have never customized). Callers resolve the default layout.
var ErrLayoutNotFound = errors.New("dashboard: layout not found")

// Bounds on a layout. They exist to keep one user's blob small and the web
// surface navigable, not because anything below breaks past them.
const (
	// MaxSections caps how many sections one layout may hold.
	MaxSections = 12
	// MaxSectionTitleLen caps a section title, in runes.
	MaxSectionTitleLen = 40
)

// Section is one named group of tiles within a layout.
//
// ID is opaque to the server: the client generates it and it only has to be
// unique and stable within the one layout that holds it (it keys the web
// surface's React list and its drag targets). Title may be empty — an untitled
// section renders as a bare grid with no header and no rule, which is the shape
// every pre-055 layout migrated into.
type Section struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Collapsed bool     `json:"collapsed"`
	TileIDs   []TileID `json:"tile_ids"`
}

// Layout is a user's persisted dashboard: the ordered sections and, within
// each, the ordered tiles. On read it is normalized (see Normalize), so a
// Layout handed to callers always satisfies every invariant regardless of what
// was stored.
type Layout struct {
	Sections []Section
}

// TileIDs flattens the layout to the tiles it enables, in display order. The
// summary read path uses this: which tiles to build is a set question, and
// section structure never reaches the builders.
func (l Layout) TileIDs() []TileID {
	out := make([]TileID, 0, len(l.Sections))
	for _, s := range l.Sections {
		out = append(out, s.TileIDs...)
	}
	return out
}

// Normalize enforces every layout invariant, returning a Layout safe to serve
// and to render. It is applied on read (a stored blob can predate a rule, or
// name a tile that has since been retired) and after decoding a write body.
//
// It is deliberately total — it repairs rather than rejects — because the read
// path must never fail on data the write path once accepted. The write path
// gets its errors from validateSections instead, which runs BEFORE this and
// rejects what a client should not have sent.
//
// The repairs, in order:
//
//   - RETIRED tile ids become the tile that absorbed them (see RetiredTiles),
//     so a merged tile keeps its slot instead of vanishing from the layout;
//   - unknown tile ids are dropped (the catalog is the source of truth);
//   - a tile appearing more than once anywhere in the layout is kept at its
//     first occurrence only — rendering it twice would desync on remove;
//   - titles are trimmed, and truncated to MaxSectionTitleLen runes;
//   - empty or duplicated section ids are replaced with a positional fallback;
//   - sections past MaxSections are dropped;
//   - an empty layout becomes one untitled empty section, so no caller ever
//     handles a sectionless layout.
func Normalize(sections []Section) Layout {
	seenTile := make(map[TileID]bool)
	seenID := make(map[string]bool)
	out := make([]Section, 0, len(sections))

	for i, s := range resolveSectionTiles(sections) {
		if len(out) >= MaxSections {
			break
		}
		tiles := make([]TileID, 0, len(s.TileIDs))
		for _, id := range s.TileIDs {
			if !ValidTileID(id) || seenTile[id] {
				continue
			}
			seenTile[id] = true
			tiles = append(tiles, id)
		}
		id := s.ID
		if id == "" || seenID[id] {
			id = "s" + strconv.Itoa(i+1)
			for seenID[id] {
				id += "x"
			}
		}
		seenID[id] = true
		out = append(out, Section{
			ID:        id,
			Title:     truncateRunes(strings.TrimSpace(s.Title), MaxSectionTitleLen),
			Collapsed: s.Collapsed,
			TileIDs:   tiles,
		})
	}

	if len(out) == 0 {
		out = []Section{{ID: "s1", TileIDs: []TileID{}}}
	}
	return Layout{Sections: out}
}

// SingleSection wraps an ordered tile list into the one-untitled-section layout
// that a pre-055 layout migrates to. It backs the default layout and the write
// path's tolerant reader.
func SingleSection(tileIDs []TileID) []Section {
	if tileIDs == nil {
		tileIDs = []TileID{}
	}
	return []Section{{ID: "s1", TileIDs: tileIDs}}
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// Repository persists one dashboard layout per user, keyed by user_id.
type Repository interface {
	// Get returns the stored layout, normalized. ErrLayoutNotFound when the
	// user has no row.
	Get(ctx context.Context, userID string) (Layout, error)
	// Upsert writes the ordered sections for the user (insert or replace).
	Upsert(ctx context.Context, userID string, sections []Section) error
}
