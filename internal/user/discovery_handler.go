package user

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// listLimitDefault / listLimitMax bound the page size for the discovery list and
// search endpoints, mirroring the timeline feed's and follow inbox's 20/50.
const (
	listLimitDefault = 20
	listLimitMax     = 50
)

// FollowReader is the slice of the follow domain the discovery handler consumes:
// the read-only counts, relationship lookups, and accepted-edge lists. The
// follow package does NOT import user (no cycle), so this interface is declared
// here and the server passes the concrete follow repository (which already
// satisfies it) directly — mirroring the cross-domain read seam pattern.
type FollowReader interface {
	CountFollowers(ctx context.Context, userID string) (int, error)
	CountFollowing(ctx context.Context, userID string) (int, error)
	Relationship(ctx context.Context, viewerID, otherID string) (follow.Relationship, error)
	Relationships(ctx context.Context, viewerID string, otherIDs []string) (map[string]follow.Relationship, error)
	ListFollowers(ctx context.Context, followeeID string, limit int, before *follow.Cursor) ([]follow.Follow, *follow.Cursor, error)
	ListFollowing(ctx context.Context, followerID string, limit int, before *follow.Cursor) ([]follow.Follow, *follow.Cursor, error)
}

// DiscoveryHandler serves the public discovery surface: a user's public profile
// (with follow counts + the viewer's relationship), their followers/following
// lists, and ranked profile search. It is intentionally separate from the /me
// Handler — that one owns the authed user's own account; this one owns the
// cross-user read views. It reads follow data through the injected FollowReader
// and resolves avatars through the (possibly nil) AvatarStore, the same way the
// /me handler does.
type DiscoveryHandler struct {
	repo    Repository
	follows FollowReader
	// store is the avatar object store; may be nil (no presign attempted — the
	// avatar_url falls back to the OAuth URL or null), matching the /me handler.
	store AvatarStore
	// lifts / runs are the cross-domain read seams the profile-stats endpoint
	// consumes: a user's completed lift sessions and running samples. They are
	// declared as narrow interfaces local to this package so user never imports
	// workout/activity; the server passes thin adapters over the real repos.
	lifts LiftSessionSource
	runs  RunningSampleSource
}

// NewDiscoveryHandler constructs the discovery handler over the user repository,
// the follow read seam, the (possibly nil) avatar store, and the lift/running
// stat sources that back GET /users/{username}/stats.
func NewDiscoveryHandler(repo Repository, follows FollowReader, store AvatarStore, lifts LiftSessionSource, runs RunningSampleSource) *DiscoveryHandler {
	return &DiscoveryHandler{repo: repo, follows: follows, store: store, lifts: lifts, runs: runs}
}

// Mount registers the discovery routes on the JWT-gated group. /users/search is
// registered before /users/{username} so the static segment wins; chi prefers
// static over wildcard segments regardless, but the explicit order documents
// the intent and is verified by a test.
func (h *DiscoveryHandler) Mount(r chi.Router) {
	r.Get("/users/search", h.search)
	r.Get("/users/{username}", h.getProfile)
	r.Get("/users/{username}/followers", h.listFollowers)
	r.Get("/users/{username}/following", h.listFollowing)
	r.Get("/users/{username}/stats", h.getStats)
}

// --- DTOs ----------------------------------------------------------------

// profileSummaryDTO is the shared shape for a user row in the followers,
// following, and search responses: the public profile slice plus the viewer's
// relationship to that user. Reused so the three list/search surfaces render an
// identical row.
type profileSummaryDTO struct {
	UserID       string              `json:"user_id"`
	Username     *string             `json:"username"`
	DisplayName  string              `json:"display_name"`
	AvatarURL    *string             `json:"avatar_url"`
	Relationship follow.Relationship `json:"relationship"`
}

// publicProfileDTO is the GET /users/{username} payload: the public profile
// fields plus follow counts and the viewer's relationship. It deliberately does
// NOT include activities — the activity feed is gated and served by timeline.
type publicProfileDTO struct {
	UserID         string              `json:"user_id"`
	Username       *string             `json:"username"`
	DisplayName    string              `json:"display_name"`
	Bio            *string             `json:"bio"`
	AvatarURL      *string             `json:"avatar_url"`
	FollowerCount  int                 `json:"follower_count"`
	FollowingCount int                 `json:"following_count"`
	Relationship   follow.Relationship `json:"relationship"`
}

// listResponse is the shared envelope for the followers, following, and search
// pages: a list of summary rows plus the opaque keyset cursor. next_cursor is
// null when the list is exhausted.
type listResponse struct {
	Users      []profileSummaryDTO `json:"users"`
	NextCursor *string             `json:"next_cursor"`
}

// --- cursor codecs -------------------------------------------------------

// encodeFollowCursor renders a follow.Cursor as the opaque token used by the
// followers/following lists: base64url(RawURLEncoding) of
// `<RFC3339Nano created_at>|<id>`, matching the follow/timeline codec.
func encodeFollowCursor(c follow.Cursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeFollowCursor parses a followers/following list cursor token. Any
// malformed token is a client error the handler maps to 400 "invalid cursor".
func decodeFollowCursor(token string) (follow.Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return follow.Cursor{}, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return follow.Cursor{}, errors.New("user: malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return follow.Cursor{}, err
	}
	return follow.Cursor{CreatedAt: t.UTC(), ID: parts[1]}, nil
}

// encodeSearchCursor renders a SearchCursor as the opaque search-page token.
// The sort key is arbitrary user text (it may contain any byte), so the three
// components are length-prefixed — `<bucket>|<len(sortkey)>|<sortkey><id>` —
// rather than naively delimited, so a sort key containing the delimiter can't
// corrupt the parse. The whole thing is base64url'd to stay opaque/URL-safe.
func encodeSearchCursor(c SearchCursor) string {
	raw := strconv.Itoa(c.Bucket) + "|" + strconv.Itoa(len(c.SortKey)) + "|" + c.SortKey + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeSearchCursor parses a search-page token back into a SearchCursor,
// reversing encodeSearchCursor's length-prefixed framing. Any malformed token
// is a client error mapped to 400 "invalid cursor".
func decodeSearchCursor(token string) (SearchCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return SearchCursor{}, err
	}
	s := string(b)
	// bucket
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return SearchCursor{}, errors.New("user: malformed search cursor")
	}
	bucket, err := strconv.Atoi(s[:i])
	if err != nil {
		return SearchCursor{}, err
	}
	rest := s[i+1:]
	// len(sortkey)
	j := strings.IndexByte(rest, '|')
	if j < 0 {
		return SearchCursor{}, errors.New("user: malformed search cursor")
	}
	keyLen, err := strconv.Atoi(rest[:j])
	if err != nil || keyLen < 0 {
		return SearchCursor{}, errors.New("user: malformed search cursor")
	}
	payload := rest[j+1:]
	if keyLen > len(payload) {
		return SearchCursor{}, errors.New("user: malformed search cursor")
	}
	return SearchCursor{Bucket: bucket, SortKey: payload[:keyLen], ID: payload[keyLen:]}, nil
}

// --- handlers ------------------------------------------------------------

// getProfile handles GET /users/{username}: a public profile with follow counts
// and the viewer's relationship. Resolves via GetByUsername (404 on unknown).
func (h *DiscoveryHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.viewer(w, r)
	if !ok {
		return
	}
	username := chi.URLParam(r, "username")
	u, err := h.repo.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "user not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user by username", err)
		return
	}

	followers, err := h.follows.CountFollowers(r.Context(), u.ID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count followers", err)
		return
	}
	following, err := h.follows.CountFollowing(r.Context(), u.ID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "count following", err)
		return
	}

	rel := follow.RelationshipSelf
	if u.ID != viewer {
		rel, err = h.follows.Relationship(r.Context(), viewer, u.ID)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "relationship", err)
			return
		}
	}

	httpresp.OK(w, "got profile", publicProfileDTO{
		UserID:         u.ID,
		Username:       u.Username,
		DisplayName:    u.DisplayName,
		Bio:            u.Bio,
		AvatarURL:      h.avatarURL(r.Context(), u),
		FollowerCount:  followers,
		FollowingCount: following,
		Relationship:   rel,
	})
}

// listFollowers handles GET /users/{username}/followers: a keyset page of the
// user's accepted followers, each row decorated with the viewer's relationship.
// Public — any authed viewer may read it. 404 on unknown username.
func (h *DiscoveryHandler) listFollowers(w http.ResponseWriter, r *http.Request) {
	h.listEdges(w, r, h.follows.ListFollowers, func(f follow.Follow) string { return f.FollowerID })
}

// listFollowing handles GET /users/{username}/following: same as listFollowers
// but over the user's accepted followees.
func (h *DiscoveryHandler) listFollowing(w http.ResponseWriter, r *http.Request) {
	h.listEdges(w, r, h.follows.ListFollowing, func(f follow.Follow) string { return f.FolloweeID })
}

// listEdges is the shared body of the followers/following endpoints: resolve the
// {username} owner, page the accepted edges via the given list function, extract
// the "other" id per row via otherID, hydrate summaries, and render.
func (h *DiscoveryHandler) listEdges(
	w http.ResponseWriter,
	r *http.Request,
	list func(ctx context.Context, ownerID string, limit int, before *follow.Cursor) ([]follow.Follow, *follow.Cursor, error),
	otherID func(follow.Follow) string,
) {
	viewer, ok := h.viewer(w, r)
	if !ok {
		return
	}
	owner, ok := h.resolveOwner(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	var before *follow.Cursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := decodeFollowCursor(raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		before = &c
	}

	rows, next, err := list(r.Context(), owner.ID, limit, before)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list edges", err)
		return
	}

	ids := make([]string, 0, len(rows))
	for _, f := range rows {
		ids = append(ids, otherID(f))
	}

	summaries, err := h.summaries(r.Context(), viewer, ids)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build summaries", err)
		return
	}

	var nextCursor *string
	if next != nil {
		token := encodeFollowCursor(*next)
		nextCursor = &token
	}
	httpresp.OK(w, "listed users", listResponse{Users: summaries, NextCursor: nextCursor})
}

// search handles GET /users/search?q=&limit=&cursor=: ranked profile search.
// The searcher's own row is included (relationship self). 400 on a bad cursor.
func (h *DiscoveryHandler) search(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.viewer(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	var after *SearchCursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := decodeSearchCursor(raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		after = &c
	}

	results, next, err := h.repo.SearchProfiles(r.Context(), q, limit, after)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "search profiles", err)
		return
	}

	ids := make([]string, 0, len(results))
	for _, u := range results {
		ids = append(ids, u.ID)
	}
	rels, err := h.follows.Relationships(r.Context(), viewer, ids)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "relationships", err)
		return
	}
	summaries := make([]profileSummaryDTO, 0, len(results))
	for _, u := range results {
		summaries = append(summaries, profileSummaryDTO{
			UserID:       u.ID,
			Username:     u.Username,
			DisplayName:  u.DisplayName,
			AvatarURL:    h.avatarURL(r.Context(), u),
			Relationship: rels[u.ID],
		})
	}

	var nextCursor *string
	if next != nil {
		token := encodeSearchCursor(*next)
		nextCursor = &token
	}
	httpresp.OK(w, "searched profiles", listResponse{Users: summaries, NextCursor: nextCursor})
}

// --- helpers -------------------------------------------------------------

// summaries hydrates a list of user ids into profile-summary rows decorated with
// the viewer's relationship to each, batching the relationship lookup (no N+1).
// A missing id (deleted out from under the edge) is skipped.
func (h *DiscoveryHandler) summaries(ctx context.Context, viewer string, ids []string) ([]profileSummaryDTO, error) {
	out := make([]profileSummaryDTO, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rels, err := h.follows.Relationships(ctx, viewer, ids)
	if err != nil {
		return nil, err
	}
	for _, oid := range ids {
		u, err := h.repo.GetByID(ctx, oid)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, profileSummaryDTO{
			UserID:       u.ID,
			Username:     u.Username,
			DisplayName:  u.DisplayName,
			AvatarURL:    h.avatarURL(ctx, u),
			Relationship: rels[oid],
		})
	}
	return out, nil
}

// avatarURL resolves a user's avatar URL: a presigned GET of the uploaded
// avatar, the OAuth fallback, or nil — mirroring Handler.resolveMe and
// nil-guarding the store.
func (h *DiscoveryHandler) avatarURL(ctx context.Context, u *User) *string {
	switch {
	case u.AvatarKey != nil && h.store != nil:
		url, err := h.store.PresignGet(ctx, *u.AvatarKey)
		if err != nil {
			log.Printf("discovery: avatar presign user_id=%s key=%s err=%v", u.ID, *u.AvatarKey, err)
			return u.OAuthAvatarURL // graceful fallback
		}
		return &url
	case u.OAuthAvatarURL != nil:
		return u.OAuthAvatarURL
	default:
		return nil
	}
}

// viewer reads the authed viewer id from context, writing a 500 (auth
// middleware not applied) and returning ok=false on absence.
func (h *DiscoveryHandler) viewer(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return "", false
	}
	return id, true
}

// resolveOwner resolves the {username} path param to its user, writing a 404 on
// an unknown handle and returning ok=false.
func (h *DiscoveryHandler) resolveOwner(w http.ResponseWriter, r *http.Request) (*User, bool) {
	username := chi.URLParam(r, "username")
	u, err := h.repo.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "user not found", "not_found")
			return nil, false
		}
		httpresp.ServerError(w, r.Context(), "get user by username", err)
		return nil, false
	}
	return u, true
}

// parseLimit reads the limit query param (default 20, cap 50), writing a 400 on
// a non-positive value and returning ok=false.
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := listLimitDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpresp.Error(w, http.StatusBadRequest, "limit must be a positive integer")
			return 0, false
		}
		limit = n
		if limit > listLimitMax {
			limit = listLimitMax
		}
	}
	return limit, true
}
