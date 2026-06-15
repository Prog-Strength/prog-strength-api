package beta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Handler serves the admin allowlist surface at /admin/beta-emails. It needs
// the user repository to resolve the calling admin's email (recorded in
// added_by) from the user ID that the auth middleware injects into context.
//
// The handler deliberately does NOT import the auth package — the admin gate
// (auth.RequireAdmin) is applied by the enclosing router group in server.go,
// avoiding the import cycle that would arise from auth importing beta (for
// Checker) while beta imported auth.
type Handler struct {
	repo  Repository
	users user.Repository
}

func NewHandler(repo Repository, users user.Repository) *Handler {
	return &Handler{repo: repo, users: users}
}

// Mount registers the three admin routes WITHOUT an admin gate. The caller is
// responsible for wrapping these in an auth.RequireAdmin group (see
// server.go); they read the caller's id from a context that RequireUser +
// RequireAdmin have already populated and authorized.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/admin/beta-emails", h.list)
	r.Post("/admin/beta-emails", h.add)
	r.Delete("/admin/beta-emails/{email}", h.remove)
}

// listResponse is the GET payload: the full allowlist, sorted by added_at asc.
type listResponse struct {
	Emails []AllowedEmail `json:"emails"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	emails, err := h.repo.List(r.Context())
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list beta emails", err)
		return
	}
	if emails == nil {
		emails = []AllowedEmail{}
	}
	httpresp.OK(w, "listed beta emails", listResponse{Emails: emails})
}

// addRequest is the POST body. note is optional free-text.
type addRequest struct {
	Email string `json:"email"`
	Note  string `json:"note"`
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := normalizeEmail(req.Email)
	if email == "" {
		httpresp.Error(w, http.StatusBadRequest, "email is required")
		return
	}

	// Determine pre-existence to choose the status code. IsAllowed can't be
	// used here: it returns true for ANY email when the table is empty, so it
	// can't distinguish "this specific email is present" from "the gate is
	// off". Use a membership-specific lookup against List instead.
	existing, err := h.find(r.Context(), email)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "check beta email", err)
		return
	}

	// Resolve the calling admin's email for added_by. Best-effort: if the
	// caller can't be resolved we still record the row (added_by NULL) rather
	// than failing an otherwise-valid add.
	addedBy := h.callerEmail(r)

	if err = h.repo.Add(r.Context(), email, addedBy, req.Note); err != nil {
		httpresp.ServerError(w, r.Context(), "add beta email", err)
		return
	}

	row, err := h.find(r.Context(), email)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "load beta email", err)
		return
	}

	if existing != nil {
		httpresp.OK(w, "beta email already present", row)
		return
	}
	httpresp.Created(w, "added beta email", row)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "email")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid email path parameter")
		return
	}
	email := normalizeEmail(decoded)
	if email == "" {
		httpresp.Error(w, http.StatusBadRequest, "email is required")
		return
	}

	removed, err := h.repo.Remove(r.Context(), email)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "remove beta email", err)
		return
	}
	if !removed {
		httpresp.Error(w, http.StatusNotFound, "beta email not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// callerEmail resolves the calling admin's email from the context user ID for
// the added_by audit field. Returns "" (stored as NULL) when the caller can't
// be resolved — the add still succeeds.
func (h *Handler) callerEmail(r *http.Request) string {
	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		return ""
	}
	u, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		return ""
	}
	return u.Email
}

// find returns the row for the normalized email, or nil if absent. It scans
// List rather than calling IsAllowed because IsAllowed is allow-all on an
// empty table and so can't answer the membership-specific question.
func (h *Handler) find(ctx context.Context, email string) (*AllowedEmail, error) {
	emails, err := h.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range emails {
		if emails[i].Email == email {
			return &emails[i], nil
		}
	}
	return nil, nil
}
