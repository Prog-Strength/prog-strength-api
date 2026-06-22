package snapshot

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/daterange"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes the snapshot Service over HTTP. It owns no domain logic:
// it parses the date window, then delegates to Service.Build.
type Handler struct {
	svc *Service

	// now sources the current instant for the default trailing-window math.
	// It defaults to time.Now; tests override it to pin a fixed reference time.
	now func() time.Time
}

// NewHandler builds a snapshot Handler backed by an already-constructed
// Service (the caller wires the Service from the concrete repositories).
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, now: time.Now}
}

// Mount registers the snapshot route. Callers must have already wrapped the
// router in auth.RequireUser — snapshot reads the user ID from context.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/training-snapshot", func(r chi.Router) {
		r.Get("/", h.snapshot)
	})
}

// snapshot handles GET /training-snapshot. A `timezone` query param (IANA
// name) is always required. With no date params it defaults to the trailing
// 7 local days ending today; otherwise it honors `date` or
// `start_date`+`end_date` via the shared daterange contract.
func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	q := r.URL.Query()
	var start, end time.Time
	var loc *time.Location
	var err error

	if q.Get("date") == "" && q.Get("start_date") == "" && q.Get("end_date") == "" {
		// Default window: trailing 7 local days ending today.
		tz := q.Get("timezone")
		if tz == "" {
			httpresp.Error(w, http.StatusBadRequest, "timezone is required")
			return
		}
		loc, err = daterange.LoadTimezone(tz)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid timezone "+tz)
			return
		}
		now := h.now().In(loc)
		endLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		startLocal := endLocal.AddDate(0, 0, -7)
		start, end = startLocal.UTC(), endLocal.UTC()
	} else {
		start, end, loc, err = daterange.ParseQuery(q)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	snap := h.svc.Build(ctx, userID, start, end, loc)
	httpresp.OK(w, "training snapshot", snap)
}
