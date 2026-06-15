package steps

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// dateLayout is the canonical wire + storage format for a step day. Both
// the {date} path param and the since/until/before query params use it;
// lexicographic order on this layout is calendar order.
const dateLayout = "2006-01-02"

// Handler serves /steps: GET lists (range or keyset), PUT upserts a day's
// total, and DELETE hard-deletes it. Steps are unitless, so unlike
// bodyweight the handler needs no user repository to default a unit.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers routes on the given router. Callers are expected to have
// already wrapped the router in auth.RequireUser — these handlers read the
// user ID out of request context.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/steps", func(r chi.Router) {
		r.Get("/", h.list)
		r.Put("/{date}", h.upsert)
		r.Delete("/{date}", h.delete)
	})
	// Per-user steps goal. Lives on /me/... to match the bodyweight-goal
	// and macro-goals convention (one row per user, no listing,
	// set-replacement semantics).
	r.Route("/me/steps-goal", func(r chi.Router) {
		r.Get("/", h.getMyStepsGoal)
		r.Put("/", h.putMyStepsGoal)
	})
}

// --- DTOs ----------------------------------------------------------

type entryDTO struct {
	ID        string    `json:"id"`
	Date      string    `json:"date"`
	Steps     int       `json:"steps"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toDTO(e Entry) entryDTO {
	return entryDTO{
		ID:        e.ID,
		Date:      e.Date,
		Steps:     e.Steps,
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}

// listDTO is the wire shape for GET /steps. NextBefore is the keyset cursor
// for the next page (null when there are no more rows / in range mode).
type listDTO struct {
	Steps      []entryDTO `json:"steps"`
	NextBefore *string    `json:"next_before"`
}

type upsertRequest struct {
	Steps int `json:"steps"`
}

// --- Handlers ------------------------------------------------------

// list handles GET /steps in two modes. limit set → keyset mode (up to
// limit rows with date < before, newest first), and keyset wins over
// since/until. Otherwise range mode (since <= date <= until, both
// inclusive). The response data is {steps, next_before}.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	q := r.URL.Query()

	var (
		since, until, before *string
		limit                int
	)

	// limit, when present, selects keyset mode and must be a positive int.
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			httpresp.Error(w, http.StatusBadRequest, "invalid limit (expected positive integer)")
			return
		}
		limit = n
	}

	if limit > 0 {
		// Keyset mode: only before is honored; since/until are ignored.
		if raw := q.Get("before"); raw != "" {
			if err := validateDate(raw); err != nil {
				httpresp.Error(w, http.StatusBadRequest, "invalid before (expected YYYY-MM-DD)")
				return
			}
			before = &raw
		}
	} else {
		// Range mode: since/until bound the date inclusively.
		if raw := q.Get("since"); raw != "" {
			if err := validateDate(raw); err != nil {
				httpresp.Error(w, http.StatusBadRequest, "invalid since (expected YYYY-MM-DD)")
				return
			}
			since = &raw
		}
		if raw := q.Get("until"); raw != "" {
			if err := validateDate(raw); err != nil {
				httpresp.Error(w, http.StatusBadRequest, "invalid until (expected YYYY-MM-DD)")
				return
			}
			until = &raw
		}
	}

	entries, nextBefore, err := h.repo.List(r.Context(), userID, since, until, limit, before)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list steps", err)
		return
	}

	out := listDTO{Steps: make([]entryDTO, 0, len(entries))}
	for _, e := range entries {
		out.Steps = append(out.Steps, toDTO(e))
	}
	if nextBefore != "" {
		out.NextBefore = &nextBefore
	}
	httpresp.OK(w, "listed steps", out)
}

// upsert handles PUT /steps/{date}. The {date} path param is the calendar
// day; the body carries the count. Re-logging a day overwrites its total.
// Validation (date shape, future bound, step range) produces clean 400s.
func (h *Handler) upsert(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	date := chi.URLParam(r, "date")
	if err := validateUpsertDate(date); err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	var req upsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Steps < 0 || req.Steps > MaxSteps {
		httpresp.Error(w, http.StatusBadRequest, ErrStepsOutOfRange.Error())
		return
	}

	saved, err := h.repo.UpsertEntry(r.Context(), &Entry{
		UserID: userID,
		Date:   date,
		Steps:  req.Steps,
	})
	if err != nil {
		if errors.Is(err, ErrStepsOutOfRange) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "upsert steps", err)
		return
	}
	httpresp.OK(w, "logged steps", toDTO(saved))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	date := chi.URLParam(r, "date")
	if err := validateDate(date); err != nil {
		httpresp.Error(w, http.StatusBadRequest, ErrInvalidDate.Error())
		return
	}
	if err := h.repo.Delete(r.Context(), userID, date); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "steps entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete steps", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers -------------------------------------------------------

// validateDate checks that s parses as a YYYY-MM-DD calendar date.
func validateDate(s string) error {
	if _, err := time.Parse(dateLayout, s); err != nil {
		return ErrInvalidDate
	}
	return nil
}

// validateUpsertDate enforces both the date shape and the future bound:
// the day must parse and be no more than one day ahead of the current UTC
// date. One day of slack tolerates timezone midnight crossings; anything
// further is rejected as a typo.
func validateUpsertDate(s string) error {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		return ErrInvalidDate
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if d.After(today.AddDate(0, 0, 1)) {
		return ErrDateTooFarInFuture
	}
	return nil
}
