package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
)

// putLayout handles PUT /dashboard/layout — the write path for a user's
// customized tile layout. The body is the full ordered set of enabled tile ids
// (a replace, not a patch). Validation is first-error-wins at the boundary:
// unknown or duplicate ids are rejected before any persistence. An empty list
// is a legitimate preference (a bare dashboard), so it persists as-is.
func (h *Handler) putLayout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req struct {
		TileIDs []TileID `json:"tile_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg, ok := validateTileIDs(req.TileIDs); !ok {
		httpresp.Error(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if err := h.layoutRepo.Upsert(ctx, userID, req.TileIDs); err != nil {
		httpresp.ServerError(w, ctx, "upsert dashboard layout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateTileIDs rejects unknown ids and duplicates. An empty (or nil) list is
// valid — an empty dashboard is a legitimate preference.
func validateTileIDs(ids []TileID) (string, bool) {
	seen := make(map[TileID]bool, len(ids))
	for _, id := range ids {
		if !ValidTileID(id) {
			return "unknown tile id " + string(id) + "; valid ids: " + validIDList(), false
		}
		if seen[id] {
			return "duplicate tile id " + string(id), false
		}
		seen[id] = true
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
