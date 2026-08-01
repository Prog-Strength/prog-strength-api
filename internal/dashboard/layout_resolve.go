package dashboard

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
)

// resolveLayout returns the user's stored layout, or the default when none is
// stored. A layout-read failure (other than not-found) is logged and degrades
// to the default rather than failing the request — one flaky table can never
// blank the dashboard, the same principle as the per-section defer1 reads.
func (h *Handler) resolveLayout(ctx context.Context, r *http.Request, userID string) []TileID {
	l, err := h.layoutRepo.Get(ctx, userID)
	if err == nil {
		return l.TileIDs
	}
	if !errors.Is(err, ErrLayoutNotFound) {
		log.Printf("dashboard: layout for %s: %v", requestid.FromContext(r.Context()), err)
	}
	return h.defaultLayout(ctx, r, userID)
}

// defaultLayout reproduces today's dashboard: running, lifting, steps,
// nutrition, bodyweight, [recovery,] streak. Recovery is included only when the
// user has a connected Whoop connection — a non-Whoop user should not land on an
// empty Recovery card they never asked for.
func (h *Handler) defaultLayout(ctx context.Context, r *http.Request, userID string) []TileID {
	ids := []TileID{TileRunning, TileLifting, TileSteps, TileNutrition, TileBodyweight}
	if h.hasConnectedWhoop(ctx, r, userID) {
		ids = append(ids, TileRecovery)
	}
	return append(ids, TileStreak)
}

// hasConnectedWhoop reports whether the user has a CONNECTED Whoop connection.
// A missing/errored connection reads as false (recovery stays out of the default
// and buildRecoverySection independently returns nil).
func (h *Handler) hasConnectedWhoop(ctx context.Context, r *http.Request, userID string) bool {
	conn, err := h.whoopConns.Get(ctx, userID)
	if err != nil {
		if !errors.Is(err, whoopconn.ErrNotFound) {
			log.Printf("dashboard: whoop connection for %s: %v", requestid.FromContext(r.Context()), err)
		}
		return false
	}
	return conn.Status == whoopconn.StatusConnected
}
