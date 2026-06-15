package follow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// requestsLimitDefault / requestsLimitMax bound the page size for the requests
// inbox, mirroring the timeline feed's 20/50 defaults.
const (
	requestsLimitDefault = 20
	requestsLimitMax     = 50
)

// ProfileSummary is the slice of a user's profile the follow surface renders
// alongside each edge. It is the seam's output: the follow package never
// imports the user domain, so the wiring layer adapts user.Repository into
// this shape.
type ProfileSummary struct {
	UserID      string
	DisplayName string
	Username    *string
	AvatarURL   *string
}

// ProfileProvider is the user-domain seam the follow handler depends on. It
// resolves usernames to ids, checks existence, and batch-loads profile
// summaries — all without the follow package importing the user package.
type ProfileProvider interface {
	// ResolveUsername maps a username to its user id, returning ErrNotFound
	// (the follow package's sentinel) when no such user exists.
	ResolveUsername(ctx context.Context, username string) (userID string, err error)
	// UserExists reports whether a user id refers to a live user.
	UserExists(ctx context.Context, userID string) (bool, error)
	// ProfileSummaries batch-loads the summaries for the given ids, keyed by id.
	// Missing ids are simply absent from the map.
	ProfileSummaries(ctx context.Context, userIDs []string) (map[string]ProfileSummary, error)
}

// Handler exposes the HTTP surface for the follow graph: the request/accept
// state machine, the teardown verbs, and the requests inbox. It owns no user
// data — profile summaries are loaded at read time through the injected
// ProfileProvider, so the follow package stays free of any user-domain import.
type Handler struct {
	repo     Repository
	profiles ProfileProvider
}

func NewHandler(repo Repository, profiles ProfileProvider) *Handler {
	return &Handler{repo: repo, profiles: profiles}
}

// Mount registers the follow routes. Callers are expected to have wrapped the
// router in the auth middleware — these handlers read the actor's id from
// request context and assume it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/follows", h.create)
	r.Post("/follows/{username}/accept", h.accept)
	r.Post("/follows/{username}/reject", h.reject)
	r.Delete("/follows/{username}", h.unfollow)
	r.Delete("/followers/{username}", h.removeFollower)
	r.Get("/follows/requests", h.listRequests)
}

// --- cursor codec --------------------------------------------------------

// encodeCursor renders a keyset position as the opaque cursor token:
// base64url of `<RFC3339Nano created_at>|<id>`, matching the timeline codec.
func encodeCursor(c Cursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses an opaque cursor token back into a Cursor. Any malformed
// token is a client error the handler maps to 400 "invalid cursor".
func decodeCursor(token string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return Cursor{}, errors.New("follow: malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{CreatedAt: t.UTC(), ID: parts[1]}, nil
}

// --- DTOs ----------------------------------------------------------------

// edgeDTO is the wire shape of a single follow edge from the actor's view: the
// other user's profile summary plus the actor's computed relationship to them.
type edgeDTO struct {
	UserID       string       `json:"user_id"`
	Username     *string      `json:"username"`
	DisplayName  string       `json:"display_name"`
	AvatarURL    *string      `json:"avatar_url"`
	Relationship Relationship `json:"relationship"`
}

// requestDTO is the wire shape of a requests-inbox row: the other user's
// summary, the actor's relationship, and the edge's created_at.
type requestDTO struct {
	UserID       string       `json:"user_id"`
	Username     *string      `json:"username"`
	DisplayName  string       `json:"display_name"`
	AvatarURL    *string      `json:"avatar_url"`
	Relationship Relationship `json:"relationship"`
	CreatedAt    time.Time    `json:"created_at"`
}

// requestsResponse is the GET /follows/requests payload: a page of inbox rows
// plus the opaque keyset cursor. next_cursor is null when the inbox is exhausted.
type requestsResponse struct {
	Requests   []requestDTO `json:"requests"`
	NextCursor *string      `json:"next_cursor"`
}

// --- handlers ------------------------------------------------------------

// create handles POST /follows: request a follow of {"followee":"<username|id>"}.
// The followee value is resolved first as a username, then (on miss) treated as
// a raw user id and existence-checked; neither resolving is a 404.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	actor, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req struct {
		Followee string `json:"followee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Followee) == "" {
		httpresp.Error(w, http.StatusBadRequest, "followee is required")
		return
	}

	followeeID, ok := h.resolveFolloweeRef(w, r, req.Followee)
	if !ok {
		return
	}

	edge, err := h.repo.Request(r.Context(), actor, followeeID)
	if err != nil {
		switch {
		case errors.Is(err, ErrSelfFollow):
			httpresp.Error(w, http.StatusBadRequest, "cannot follow yourself")
		case errors.Is(err, ErrAlreadyExists):
			httpresp.Error(w, http.StatusConflict, "relationship already exists")
		case errors.Is(err, ErrPendingCapExceeded):
			httpresp.Error(w, http.StatusTooManyRequests, "too many outstanding follow requests")
		default:
			httpresp.ServerError(w, r.Context(), "request follow", err)
		}
		return
	}

	dto, err := h.edgeFor(r.Context(), actor, edge.FolloweeID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "render follow edge", err)
		return
	}
	httpresp.Created(w, "requested follow", dto)
}

// accept handles POST /follows/{username}/accept: the actor (followee) accepts
// a pending request authored by {username}.
func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	actor, followerID, ok := h.actorAndTarget(w, r)
	if !ok {
		return
	}
	if err := h.repo.Accept(r.Context(), actor, followerID); err != nil {
		h.writeMutationErr(w, r, "accept follow", err)
		return
	}
	dto, err := h.edgeFor(r.Context(), actor, followerID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "render follow edge", err)
		return
	}
	httpresp.OK(w, "accepted follow", dto)
}

// reject handles POST /follows/{username}/reject: the actor (followee) rejects
// a pending request authored by {username}.
func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	actor, followerID, ok := h.actorAndTarget(w, r)
	if !ok {
		return
	}
	if err := h.repo.Reject(r.Context(), actor, followerID); err != nil {
		h.writeMutationErr(w, r, "reject follow", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unfollow handles DELETE /follows/{username}: context-sensitive teardown of
// the actor's outbound edge to {username} — cancel if pending, unfollow if
// accepted, 404 if no edge.
func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	actor, targetID, ok := h.actorAndTarget(w, r)
	if !ok {
		return
	}

	edge, err := h.repo.Get(r.Context(), actor, targetID)
	if err != nil {
		h.writeMutationErr(w, r, "get follow", err)
		return
	}

	switch edge.Status {
	case StatusPending:
		err = h.repo.Cancel(r.Context(), actor, targetID)
	default:
		err = h.repo.Unfollow(r.Context(), actor, targetID)
	}
	if err != nil {
		h.writeMutationErr(w, r, "teardown follow", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeFollower handles DELETE /followers/{username}: the actor (followee)
// removes {username} from their accepted followers.
func (h *Handler) removeFollower(w http.ResponseWriter, r *http.Request) {
	actor, followerID, ok := h.actorAndTarget(w, r)
	if !ok {
		return
	}
	if err := h.repo.RemoveFollower(r.Context(), actor, followerID); err != nil {
		h.writeMutationErr(w, r, "remove follower", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listRequests handles GET /follows/requests?direction=&limit=&cursor=: the
// actor's pending requests inbox (incoming by default) or sent list (outgoing),
// newest-first, keyset-paginated, each row decorated with the other user's
// summary and the actor's relationship.
func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	actor, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "incoming"
	}
	if direction != "incoming" && direction != "outgoing" {
		httpresp.Error(w, http.StatusBadRequest, "direction must be incoming or outgoing")
		return
	}

	limit := requestsLimitDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpresp.Error(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
		if limit > requestsLimitMax {
			limit = requestsLimitMax
		}
	}

	var before *Cursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := decodeCursor(raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		before = &c
	}

	var (
		rows []Follow
		next *Cursor
		err  error
	)
	if direction == "incoming" {
		rows, next, err = h.repo.ListIncomingRequests(r.Context(), actor, limit, before)
	} else {
		rows, next, err = h.repo.ListOutgoingRequests(r.Context(), actor, limit, before)
	}
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list requests", err)
		return
	}

	// The "other" user per row is the requester (incoming) or the target
	// (outgoing).
	otherIDs := make([]string, 0, len(rows))
	for _, f := range rows {
		if direction == "incoming" {
			otherIDs = append(otherIDs, f.FollowerID)
		} else {
			otherIDs = append(otherIDs, f.FolloweeID)
		}
	}

	summaries, err := h.profiles.ProfileSummaries(r.Context(), otherIDs)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "load profile summaries", err)
		return
	}
	rels, err := h.repo.Relationships(r.Context(), actor, otherIDs)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "load relationships", err)
		return
	}

	out := make([]requestDTO, 0, len(rows))
	for i, f := range rows {
		oid := otherIDs[i]
		s := summaries[oid]
		out = append(out, requestDTO{
			UserID:       oid,
			Username:     s.Username,
			DisplayName:  s.DisplayName,
			AvatarURL:    s.AvatarURL,
			Relationship: rels[oid],
			CreatedAt:    f.CreatedAt,
		})
	}

	var nextCursor *string
	if next != nil {
		token := encodeCursor(*next)
		nextCursor = &token
	}

	httpresp.OK(w, "listed follow requests", requestsResponse{Requests: out, NextCursor: nextCursor})
}

// --- helpers -------------------------------------------------------------

// resolveFolloweeRef resolves a POST /follows body value (a username or a raw
// user id) to a user id. A value that resolves as neither is a 404. On a
// non-404 failure it writes a 500. The bool is false when a response was
// already written.
func (h *Handler) resolveFolloweeRef(w http.ResponseWriter, r *http.Request, ref string) (string, bool) {
	id, err := h.profiles.ResolveUsername(r.Context(), ref)
	if err == nil {
		return id, true
	}
	if !errors.Is(err, ErrNotFound) {
		httpresp.ServerError(w, r.Context(), "resolve username", err)
		return "", false
	}
	// Not a known username — treat the raw value as a user id.
	exists, err := h.profiles.UserExists(r.Context(), ref)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "check user exists", err)
		return "", false
	}
	if !exists {
		httpresp.ErrorWithCode(w, http.StatusNotFound, "user not found", "not_found")
		return "", false
	}
	return ref, true
}

// actorAndTarget reads the actor from context and resolves the {username} path
// param to a target user id. The bool is false when a response was already
// written (missing actor → 500, unknown username → 404).
func (h *Handler) actorAndTarget(w http.ResponseWriter, r *http.Request) (actor, target string, ok bool) {
	actor, found := authctx.UserIDFrom(r.Context())
	if !found {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return "", "", false
	}
	username := chi.URLParam(r, "username")
	if username == "" {
		httpresp.Error(w, http.StatusBadRequest, "username is required")
		return "", "", false
	}
	targetID, err := h.profiles.ResolveUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "user not found", "not_found")
			return "", "", false
		}
		httpresp.ServerError(w, r.Context(), "resolve username", err)
		return "", "", false
	}
	return actor, targetID, true
}

// writeMutationErr maps a repository mutation error to its HTTP status.
func (h *Handler) writeMutationErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpresp.ErrorWithCode(w, http.StatusNotFound, "follow not found", "not_found")
	case errors.Is(err, ErrInvalidState):
		httpresp.Error(w, http.StatusConflict, "invalid follow state")
	default:
		httpresp.ServerError(w, r.Context(), op, err)
	}
}

// edgeFor builds the edgeDTO for the actor's relationship to otherID: the other
// user's summary plus the computed relationship.
func (h *Handler) edgeFor(ctx context.Context, actor, otherID string) (edgeDTO, error) {
	summaries, err := h.profiles.ProfileSummaries(ctx, []string{otherID})
	if err != nil {
		return edgeDTO{}, err
	}
	rel, err := h.repo.Relationship(ctx, actor, otherID)
	if err != nil {
		return edgeDTO{}, err
	}
	s := summaries[otherID]
	return edgeDTO{
		UserID:       otherID,
		Username:     s.Username,
		DisplayName:  s.DisplayName,
		AvatarURL:    s.AvatarURL,
		Relationship: rel,
	}, nil
}
