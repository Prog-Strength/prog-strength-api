// Package whoopadmin exposes an admin-gated view over WHOOP connection state
// and an operator-triggered resync. It is kept out of whoopsync (whose handler
// already spans OAuth, connection CRUD, and recovery reads) because it has a
// different auth model: like beta, the admin gate is applied by the enclosing
// RequireAdmin router group in server.go, so this package does not import auth.
package whoopadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopsync"
)

// connReader is the subset of whoopconn.Repository this handler needs: list all
// connections for the admin table and fetch one by user for the detail/resync
// paths. Defining it narrowly keeps tests to trivial fakes.
type connReader interface {
	List(ctx context.Context) ([]whoopconn.Connection, error)
	Get(ctx context.Context, userID string) (*whoopconn.Connection, error)
}

// recoveryReader is the subset of whooprecovery.Repository used to enrich a
// connection view with its latest recovery date and row count.
type recoveryReader interface {
	Latest(ctx context.Context, userID string) (*whooprecovery.Entry, error)
	CountForUser(ctx context.Context, userID string) (int, error)
}

// resyncer is the subset of whoopsync.Service the resync route drives.
type resyncer interface {
	SyncSince(ctx context.Context, userID string, window time.Duration, limit int) (whoopsync.SyncResult, error)
}

// Handler serves the admin WHOOP surface. now is injectable so token_expired is
// deterministic in tests.
type Handler struct {
	conns connReader
	rec   recoveryReader
	svc   resyncer
	now   func() time.Time
}

func NewHandler(conns connReader, rec recoveryReader, svc resyncer, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{conns: conns, rec: rec, svc: svc, now: now}
}

// Mount registers the three admin routes WITHOUT an admin gate — the caller
// wraps them in an auth.RequireAdmin group (see server.go), matching beta.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/admin/whoop/connections", h.listConnections)
	r.Get("/admin/whoop/connections/{userID}", h.getConnection)
	r.Post("/admin/whoop/resync", h.resync)
}

// resyncPageLimit is the WHOOP v2 list page size; mirrors backfill.
const resyncPageLimit = 25

// Window-days bounds for resync: default when omitted, and the clamp range.
const (
	defaultWindowDays = 30
	minWindowDays     = 1
	maxWindowDays     = 90
)

// connectionView is the API contract for a single connection. JSON tags are the
// contract; latest_recovery_date is null when the user has no recovery rows.
type connectionView struct {
	UserID             string  `json:"user_id"`
	WhoopUserID        int64   `json:"whoop_user_id"`
	Status             string  `json:"status"`
	Scopes             string  `json:"scopes"`
	TokenExpiresAt     string  `json:"token_expires_at"`
	TokenExpired       bool    `json:"token_expired"`
	ConnectedAt        string  `json:"connected_at"`
	UpdatedAt          string  `json:"updated_at"`
	LatestRecoveryDate *string `json:"latest_recovery_date"` // null when no rows
	RecoveryRowCount   int     `json:"recovery_row_count"`
}

type listResponse struct {
	Connections []connectionView `json:"connections"`
}

type resyncRequest struct {
	UserID     string `json:"user_id"`
	WindowDays int    `json:"window_days"`
}

type resyncOutcome struct {
	Upserted        int `json:"upserted"`
	SkippedUnscored int `json:"skipped_unscored"`
	SkippedNoCycle  int `json:"skipped_no_cycle"`
	SkippedBadDate  int `json:"skipped_bad_date"`
}

// buildView renders a connection plus its recovery summary. Real repository
// errors are propagated so callers can answer with a 500; an absent latest
// recovery (nil, nil) is an expected empty state, not an error.
func (h *Handler) buildView(ctx context.Context, conn whoopconn.Connection) (connectionView, error) {
	view := connectionView{
		UserID:         conn.UserID,
		WhoopUserID:    conn.WhoopUserID,
		Status:         string(conn.Status),
		Scopes:         conn.Scopes,
		TokenExpiresAt: conn.TokenExpiresAt.UTC().Format(time.RFC3339),
		TokenExpired:   h.now().After(conn.TokenExpiresAt),
		ConnectedAt:    conn.ConnectedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      conn.UpdatedAt.UTC().Format(time.RFC3339),
	}

	latest, err := h.rec.Latest(ctx, conn.UserID)
	if err != nil {
		return connectionView{}, err
	}
	if latest != nil {
		view.LatestRecoveryDate = &latest.Date
	}

	count, err := h.rec.CountForUser(ctx, conn.UserID)
	if err != nil {
		return connectionView{}, err
	}
	view.RecoveryRowCount = count

	return view, nil
}

func (h *Handler) listConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.conns.List(r.Context())
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list whoop connections", err)
		return
	}

	// Non-nil so the JSON renders [] rather than null for zero connections.
	views := make([]connectionView, 0, len(conns))
	for _, conn := range conns {
		// N+1 is fine here: one row per opted-in user; revisit >10k (see SOW).
		view, err := h.buildView(r.Context(), conn)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "list whoop connections", err)
			return
		}
		views = append(views, view)
	}

	httpresp.OK(w, "listed whoop connections", listResponse{Connections: views})
}

func (h *Handler) getConnection(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")

	conn, err := h.conns.Get(r.Context(), userID)
	if err != nil {
		if errors.Is(err, whoopconn.ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "whoop connection not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get whoop connection", err)
		return
	}

	view, err := h.buildView(r.Context(), *conn)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get whoop connection", err)
		return
	}

	httpresp.OK(w, "whoop connection", view)
}

func (h *Handler) resync(w http.ResponseWriter, r *http.Request) {
	var req resyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		httpresp.Error(w, http.StatusBadRequest, "user_id is required")
		return
	}

	// Default when missing/omitted, then clamp to the supported range so an
	// operator can't ask for an unbounded backfill through this route.
	windowDays := req.WindowDays
	if windowDays <= 0 {
		windowDays = defaultWindowDays
	}
	if windowDays < minWindowDays {
		windowDays = minWindowDays
	}
	if windowDays > maxWindowDays {
		windowDays = maxWindowDays
	}

	conn, err := h.conns.Get(r.Context(), req.UserID)
	if err != nil {
		if errors.Is(err, whoopconn.ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "whoop connection not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get whoop connection", err)
		return
	}
	if conn.Status != whoopconn.StatusConnected {
		httpresp.Error(w, http.StatusConflict, fmt.Sprintf("whoop connection status is %q; reconnect required", conn.Status))
		return
	}

	res, err := h.svc.SyncSince(r.Context(), req.UserID, time.Duration(windowDays)*24*time.Hour, resyncPageLimit)
	if err != nil {
		switch {
		case errors.Is(err, whoopsync.ErrReconnectNeeded):
			httpresp.Error(w, http.StatusConflict, "whoop connection needs reconnect")
		case errors.Is(err, whoopsync.ErrUpstream):
			httpresp.Error(w, http.StatusBadGateway, "whoop api error")
		default:
			httpresp.ServerError(w, r.Context(), "resync whoop recovery", err)
		}
		return
	}

	httpresp.OK(w, "resynced whoop recovery", resyncOutcome{
		Upserted:        res.Upserted,
		SkippedUnscored: res.SkippedUnscored,
		SkippedNoCycle:  res.SkippedNoCycle,
		SkippedBadDate:  res.SkippedBadDate,
	})
}
