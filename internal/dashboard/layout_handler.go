package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
)

// putLayout handles PUT /dashboard/layout — the write path for a user's
// customized dashboard. The body is the full ordered set of sections (a
// replace, not a patch). Validation is first-error-wins at the boundary: an
// invalid layout is rejected before any persistence. An empty section list is a
// legitimate preference (a bare dashboard) and normalizes to one untitled empty
// section.
//
// The body may instead be a bare {"tile_ids": [...]} — the pre-055 shape. That
// is accepted and wrapped into one untitled section, so a browser tab left open
// across the deploy saves successfully instead of 422-ing. It can be dropped
// once no pre-sections client remains.
func (h *Handler) putLayout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req struct {
		Sections *[]Section `json:"sections"`
		TileIDs  *[]TileID  `json:"tile_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var sections []Section
	switch {
	case req.Sections != nil:
		sections = *req.Sections
	case req.TileIDs != nil:
		sections = SingleSection(*req.TileIDs)
	default:
		httpresp.Error(w, http.StatusBadRequest, "body must carry sections")
		return
	}

	// Resolve retired ids BEFORE validating. A client that was served the
	// catalog before a tile was merged away will keep sending the old id until
	// it reloads, and failing its save would lose the user's edit over a rename
	// they did not make. Everything else validation rejects still gets rejected.
	sections = resolveSectionTiles(sections)

	if msg, ok := validateSections(sections); !ok {
		httpresp.Error(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// Normalize AFTER validating: validation reports what the client got wrong,
	// normalization canonicalizes what it got right (trimmed titles, the
	// one-empty-section floor) so the stored blob is always in serving shape.
	if err := h.layoutRepo.Upsert(ctx, userID, Normalize(sections).Sections); err != nil {
		httpresp.ServerError(w, ctx, "upsert dashboard layout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateSections rejects what a client should never send. Tile uniqueness is
// checked across the WHOLE layout, not per section: the same tile in two
// sections would render twice and desync the moment one copy is removed.
//
// An empty list is valid — an empty dashboard is a legitimate preference.
func validateSections(sections []Section) (string, bool) {
	if len(sections) > MaxSections {
		return "too many sections: " + strconv.Itoa(len(sections)) +
			"; the maximum is " + strconv.Itoa(MaxSections), false
	}
	seenTile := make(map[TileID]bool)
	seenID := make(map[string]bool)
	for _, s := range sections {
		if strings.TrimSpace(s.ID) == "" {
			return "section id must not be empty", false
		}
		if seenID[s.ID] {
			return "duplicate section id " + s.ID, false
		}
		seenID[s.ID] = true
		if n := len([]rune(strings.TrimSpace(s.Title))); n > MaxSectionTitleLen {
			return "section title too long (" + strconv.Itoa(n) + " characters); the maximum is " +
				strconv.Itoa(MaxSectionTitleLen), false
		}
		for _, id := range s.TileIDs {
			if !ValidTileID(id) {
				return "unknown tile id " + string(id) + "; valid ids: " + validIDList(), false
			}
			if seenTile[id] {
				return "duplicate tile id " + string(id), false
			}
			seenTile[id] = true
		}
	}
	return "", true
}

func validIDList() string {
	parts := make([]string, len(Catalog))
	for i, id := range Catalog {
		parts[i] = string(id)
	}
	return strings.Join(parts, ", ")
}
