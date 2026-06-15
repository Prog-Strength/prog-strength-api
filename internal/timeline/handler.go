package timeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// feedLimitDefault / feedLimitMax bound the page size for GET /timeline. A
// page of 20 is a comfortable scroll on both clients; 50 caps the per-request
// hydration/aggregate fan-out so a single request can't pull an unbounded
// batch from the source domains.
const (
	feedLimitDefault = 20
	feedLimitMax     = 50
)

// Handler exposes the HTTP surface for the timeline: the viewer's feed, single
// posts with their comment thread, and the comment/reaction interaction
// primitives the social SOW will inherit. It owns no source content — post
// cards are rendered at read time through the injected SourceHydrator, so the
// timeline package never imports workout/activity internals.
type Handler struct {
	repo     Repository
	hydrator SourceHydrator
	// now supplies the current time; defaulted to time.Now and overridable in
	// tests. Kept for parity with the other domains' handlers even though the
	// timeline read paths are time-independent.
	now func() time.Time
}

func NewHandler(repo Repository, hydrator SourceHydrator) *Handler {
	return &Handler{repo: repo, hydrator: hydrator, now: time.Now}
}

// Mount registers routes under /timeline. Callers are expected to have already
// wrapped the router in the auth middleware — these handlers read the user ID
// from request context and assume it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/timeline", func(r chi.Router) {
		r.Get("/", h.listFeed)
		r.Route("/posts/{id}", func(r chi.Router) {
			r.Get("/", h.getPost)
			r.Post("/comments", h.addComment)
			r.Delete("/comments/{commentId}", h.deleteComment)
			r.Put("/reactions/{type}", h.addReaction)
			r.Delete("/reactions/{type}", h.removeReaction)
		})
	})
}

// --- authorization split -------------------------------------------------
//
// The two checks below are the single isolated authorization point the
// friends/followers SOW will revisit. That SOW changes ONLY canView (to admit
// posts whose author is in the viewer's followee set per the post's
// visibility); canModerate is already user-scoped and needs no change. Keeping
// them as tiny, named functions — rather than inlining `post.UserID == viewer`
// at each call site — is what makes the social change a one-line edit instead
// of an audit of every endpoint.

// canView reports whether viewerID may see post. v1: self-only (the author is
// the only viewer). A non-viewable post is reported to the client as a 404
// (ErrNotFound), never a 403, so post ids can't be enumerated cross-user.
func canView(post Post, viewerID string) bool {
	return post.UserID == viewerID
}

// canModerate reports whether viewerID may modify comment (i.e. delete it).
// v1 and beyond: ownership — only the commenter can remove their comment.
func canModerate(comment Comment, viewerID string) bool {
	return comment.UserID == viewerID
}

// --- cursor codec --------------------------------------------------------

// encodeCursor renders a keyset position as the opaque `before`/`next_before`
// token: base64url(RawURLEncoding) of `<RFC3339Nano occurred_at>|<id>`. The
// nano-precision timestamp plus the id tiebreaker reproduce the repository's
// total order exactly, so a round-tripped cursor paginates without gaps or
// repeats. RawURLEncoding keeps the token URL-safe and unpadded.
func encodeCursor(c Cursor) string {
	raw := c.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses an opaque `before` token back into a Cursor. Any
// malformed token (bad base64, missing separator, unparseable timestamp) is a
// client error the handler maps to 400 "invalid cursor" — the token is opaque,
// so a caller should only ever echo one the API handed it.
func decodeCursor(token string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return Cursor{}, errors.New("timeline: malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{OccurredAt: t.UTC(), ID: parts[1]}, nil
}

// --- DTOs ----------------------------------------------------------------

// contentDTO is the hydrated, platform-agnostic card content. metrics is
// always a non-nil slice so it serializes as [] (not null) for a source with
// no chips.
type contentDTO struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle"`
	Metrics  []string `json:"metrics"`
	Href     string   `json:"href"`
}

// reactionsDTO is the per-post reaction aggregate. summary maps reaction type
// to count; mine lists the viewer's own types (for active-state rendering).
// Both are always non-nil so they serialize as {} / [] rather than null.
type reactionsDTO struct {
	Summary map[ReactionType]int `json:"summary"`
	Mine    []ReactionType       `json:"mine"`
}

// postDTO is the wire shape of a feed post. content is the hydrated card;
// reactions and comment_count are batch-loaded for the page (no N+1).
type postDTO struct {
	ID           string       `json:"id"`
	SourceType   SourceType   `json:"source_type"`
	SourceID     string       `json:"source_id"`
	OccurredAt   time.Time    `json:"occurred_at"`
	Visibility   Visibility   `json:"visibility"`
	Content      contentDTO   `json:"content"`
	Reactions    reactionsDTO `json:"reactions"`
	CommentCount int          `json:"comment_count"`
}

// commentDTO is the wire shape of a flat comment.
type commentDTO struct {
	ID        string    `json:"id"`
	PostID    string    `json:"post_id"`
	UserID    string    `json:"user_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// feedResponse is the GET /timeline payload: a page of posts plus the opaque
// keyset cursor for the next page. next_before is null when the feed is
// exhausted.
type feedResponse struct {
	Posts      []postDTO `json:"posts"`
	NextBefore *string   `json:"next_before"`
}

// postDetailResponse is the GET /timeline/posts/{id} payload: the post in the
// same shape as a feed entry, plus its full flat comment thread (oldest-first,
// soft-deleted excluded).
type postDetailResponse struct {
	postDTO
	Comments []commentDTO `json:"comments"`
}

func toContentDTO(c PostContent) contentDTO {
	metrics := c.Metrics
	if metrics == nil {
		metrics = []string{}
	}
	return contentDTO{
		Title:    c.Title,
		Subtitle: c.Subtitle,
		Metrics:  metrics,
		Href:     c.Href,
	}
}

// toReactionsDTO shapes a post's reaction aggregate for the wire, defaulting
// the absent-post case to an empty (non-null) summary/mine.
func toReactionsDTO(s ReactionSummary) reactionsDTO {
	summary := s.Counts
	if summary == nil {
		summary = map[ReactionType]int{}
	}
	mine := s.Mine
	if mine == nil {
		mine = []ReactionType{}
	}
	return reactionsDTO{Summary: summary, Mine: mine}
}

func toCommentDTO(c Comment) commentDTO {
	return commentDTO{
		ID:        c.ID,
		PostID:    c.PostID,
		UserID:    c.UserID,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
	}
}

// refOf projects a post to the PostRef the hydrator keys content by.
func refOf(p Post) PostRef {
	return PostRef{
		UserID:     p.UserID,
		SourceType: p.SourceType,
		SourceID:   p.SourceID,
		OccurredAt: p.OccurredAt,
	}
}

// --- handlers ------------------------------------------------------------

// listFeed handles GET /timeline?limit=&before=: a keyset page of the viewer's
// own posts, newest first. It hydrates the page's content and batch-loads
// reaction summaries and comment counts so a page is assembled without an N+1
// over its posts.
func (h *Handler) listFeed(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	limit := feedLimitDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpresp.Error(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
		if limit > feedLimitMax {
			limit = feedLimitMax
		}
	}

	var before *Cursor
	if raw := r.URL.Query().Get("before"); raw != "" {
		c, err := decodeCursor(raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		before = &c
	}

	posts, next, err := h.repo.ListFeed(r.Context(), userID, limit, before)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list feed", err)
		return
	}

	dtos, err := h.assemblePosts(r.Context(), userID, posts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "assemble feed", err)
		return
	}

	var nextBefore *string
	if next != nil {
		token := encodeCursor(*next)
		nextBefore = &token
	}

	httpresp.OK(w, "listed timeline", feedResponse{Posts: dtos, NextBefore: nextBefore})
}

// assemblePosts hydrates a page of posts and decorates each with its reaction
// summary and comment count. A post whose source content is missing from the
// hydrator map (the underlying workout/run was deleted) is omitted with a log
// line rather than rendered as a broken card: the feed index is reconstructable
// and a dangling pointer is a transient state, so dropping it keeps the feed
// clean without inventing placeholder content. The returned slice is always
// non-nil so it serializes as [] for an empty/all-dangling page.
func (h *Handler) assemblePosts(ctx context.Context, viewerID string, posts []Post) ([]postDTO, error) {
	out := make([]postDTO, 0, len(posts))
	if len(posts) == 0 {
		return out, nil
	}

	refs := make([]PostRef, 0, len(posts))
	ids := make([]string, 0, len(posts))
	for _, p := range posts {
		refs = append(refs, refOf(p))
		ids = append(ids, p.ID)
	}

	content, err := h.hydrator.Hydrate(ctx, refs)
	if err != nil {
		return nil, err
	}
	summaries, err := h.repo.ReactionSummaries(ctx, ids, viewerID)
	if err != nil {
		return nil, err
	}
	counts, err := h.repo.CommentCounts(ctx, ids)
	if err != nil {
		return nil, err
	}

	for _, p := range posts {
		c, ok := content[refOf(p)]
		if !ok {
			// Source deleted out from under the index — omit-with-log so the
			// feed never shows a card with no content. Documented above.
			log.Printf("timeline: hydration missing for post id=%s source_type=%s source_id=%s — omitting", p.ID, p.SourceType, p.SourceID)
			continue
		}
		out = append(out, postDTO{
			ID:           p.ID,
			SourceType:   p.SourceType,
			SourceID:     p.SourceID,
			OccurredAt:   p.OccurredAt,
			Visibility:   p.Visibility,
			Content:      toContentDTO(c),
			Reactions:    toReactionsDTO(summaries[p.ID]),
			CommentCount: counts[p.ID],
		})
	}
	return out, nil
}

// loadViewablePost fetches a post by its {id} path param and enforces canView.
// A missing post and a non-viewable post are deliberately indistinguishable:
// both write a 404 and return ok=false. Callers that got ok=false must return
// immediately — the response is already written.
func (h *Handler) loadViewablePost(w http.ResponseWriter, r *http.Request, viewerID string) (Post, bool) {
	postID := chi.URLParam(r, "id")
	if postID == "" {
		httpresp.Error(w, http.StatusBadRequest, "post id is required")
		return Post{}, false
	}
	post, err := h.repo.GetPost(r.Context(), postID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "post not found", "not_found")
			return Post{}, false
		}
		httpresp.ServerError(w, r.Context(), "get post", err)
		return Post{}, false
	}
	if !canView(post, viewerID) {
		// Same 404 as a missing post — don't leak existence cross-user.
		httpresp.ErrorWithCode(w, http.StatusNotFound, "post not found", "not_found")
		return Post{}, false
	}
	return post, true
}

// getPost handles GET /timeline/posts/{id}: a single post in the feed shape
// plus its full flat comment thread (oldest-first, soft-deleted excluded). A
// post the viewer can't see is a 404, indistinguishable from one that doesn't
// exist.
func (h *Handler) getPost(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	post, ok := h.loadViewablePost(w, r, userID)
	if !ok {
		return
	}

	dtos, err := h.assemblePosts(r.Context(), userID, []Post{post})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "assemble post", err)
		return
	}
	if len(dtos) == 0 {
		// The single post's source content is gone — same omit-with-log
		// rationale as the feed, surfaced here as a 404 since there's nothing
		// to render.
		httpresp.ErrorWithCode(w, http.StatusNotFound, "post not found", "not_found")
		return
	}

	comments, err := h.repo.ListComments(r.Context(), post.ID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list comments", err)
		return
	}
	commentDTOs := make([]commentDTO, 0, len(comments))
	for _, c := range comments {
		commentDTOs = append(commentDTOs, toCommentDTO(c))
	}

	httpresp.OK(w, "fetched timeline post", postDetailResponse{
		postDTO:  dtos[0],
		Comments: commentDTOs,
	})
}

// addComment handles POST /timeline/posts/{id}/comments. The body must be
// non-empty and <=2000 chars (the repository re-validates as the storage-side
// backstop). canView gates participation: a viewer who can't see the post gets
// the same 404 as a missing post.
func (h *Handler) addComment(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	post, ok := h.loadViewablePost(w, r, userID)
	if !ok {
		return
	}

	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	comment, err := h.repo.AddComment(r.Context(), post.ID, userID, req.Body)
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			httpresp.Error(w, http.StatusBadRequest, "comment body must be non-empty and at most 2000 characters")
		case errors.Is(err, ErrNotFound):
			httpresp.ErrorWithCode(w, http.StatusNotFound, "post not found", "not_found")
		default:
			httpresp.ServerError(w, r.Context(), "add comment", err)
		}
		return
	}

	httpresp.Created(w, "added comment", toCommentDTO(comment))
}

// deleteComment handles DELETE /timeline/posts/{id}/comments/{commentId}. It
// soft-deletes the comment, requiring canModerate (ownership). Because the
// Repository intentionally has no GetComment, the owner is resolved by listing
// the post's live comments and matching the id — a comment that is missing or
// already soft-deleted (absent from the live list) is a 404, indistinguishable
// from a comment owned by someone else.
func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	post, ok := h.loadViewablePost(w, r, userID)
	if !ok {
		return
	}

	commentID := chi.URLParam(r, "commentId")
	if commentID == "" {
		httpresp.Error(w, http.StatusBadRequest, "comment id is required")
		return
	}

	comments, err := h.repo.ListComments(r.Context(), post.ID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list comments", err)
		return
	}
	var target *Comment
	for i := range comments {
		if comments[i].ID == commentID {
			target = &comments[i]
			break
		}
	}
	if target == nil {
		// Missing or already soft-deleted — 404, same as not-viewable.
		httpresp.ErrorWithCode(w, http.StatusNotFound, "comment not found", "not_found")
		return
	}
	if !canModerate(*target, userID) {
		// Someone else's comment. 404 (not 403) for parity with the post's
		// not-viewable masking — don't reveal ids the caller can't act on.
		httpresp.ErrorWithCode(w, http.StatusNotFound, "comment not found", "not_found")
		return
	}

	if err := h.repo.DeleteComment(r.Context(), commentID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "comment not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete comment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addReaction handles PUT /timeline/posts/{id}/reactions/{type}. The add is
// idempotent (one of each type per user per post), so it returns 200, not 201:
// PUT is the natural verb for an idempotent set-membership toggle, and a repeat
// add changes nothing — there's no "created" semantics to signal. The SOW
// permits 200/201; 200 is the simpler, consistent choice. Returns the post's
// updated reaction summary so the client can reconcile counts and active state
// without a follow-up read.
func (h *Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	post, ok := h.loadViewablePost(w, r, userID)
	if !ok {
		return
	}

	reactionType := ReactionType(chi.URLParam(r, "type"))
	if !reactionType.Valid() {
		httpresp.Error(w, http.StatusBadRequest, "unknown reaction type")
		return
	}

	if _, err := h.repo.AddReaction(r.Context(), post.ID, userID, reactionType); err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			httpresp.Error(w, http.StatusBadRequest, "unknown reaction type")
		case errors.Is(err, ErrNotFound):
			httpresp.ErrorWithCode(w, http.StatusNotFound, "post not found", "not_found")
		default:
			httpresp.ServerError(w, r.Context(), "add reaction", err)
		}
		return
	}

	httpresp.OK(w, "added reaction", h.reactionsFor(w, r, post.ID, userID))
}

// removeReaction handles DELETE /timeline/posts/{id}/reactions/{type}. It is
// idempotent: removing a reaction that isn't there is not an error. Returns 204.
func (h *Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	post, ok := h.loadViewablePost(w, r, userID)
	if !ok {
		return
	}

	reactionType := ReactionType(chi.URLParam(r, "type"))
	if !reactionType.Valid() {
		httpresp.Error(w, http.StatusBadRequest, "unknown reaction type")
		return
	}

	if err := h.repo.RemoveReaction(r.Context(), post.ID, userID, reactionType); err != nil {
		httpresp.ServerError(w, r.Context(), "remove reaction", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reactionsFor re-reads the single post's reaction summary for the response of
// a reaction write. It swallows the error into an empty summary because the
// write already succeeded — a failed re-read shouldn't turn a successful PUT
// into a 500; the client refetches on its next feed load.
func (h *Handler) reactionsFor(w http.ResponseWriter, r *http.Request, postID, viewerID string) reactionsDTO {
	summaries, err := h.repo.ReactionSummaries(r.Context(), []string{postID}, viewerID)
	if err != nil {
		log.Printf("timeline: reaction summary re-read failed for post id=%s: %v", postID, err)
		return toReactionsDTO(ReactionSummary{})
	}
	return toReactionsDTO(summaries[postID])
}
