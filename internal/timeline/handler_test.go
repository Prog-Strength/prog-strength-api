package timeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

const (
	userA = "userA"
	userB = "userB"
)

// --- fake hydrator -------------------------------------------------------

// fakeHydrator is a deterministic in-test SourceHydrator. It renders a
// PostContent per ref from the ref's own fields so assertions can pin exact
// values, and tracks the refs it was asked to render so the batch contract can
// be checked. A ref whose source id is in missing[] is omitted from the
// returned map, simulating a source deleted out from under the index.
type fakeHydrator struct {
	missing  map[string]bool
	lastRefs []PostRef
}

func (h *fakeHydrator) Hydrate(ctx context.Context, refs []PostRef) (map[PostRef]PostContent, error) {
	h.lastRefs = refs
	out := make(map[PostRef]PostContent, len(refs))
	for _, ref := range refs {
		if h.missing[ref.SourceID] {
			continue
		}
		out[ref] = PostContent{
			Title:    "title-" + ref.SourceID,
			Subtitle: "subtitle-" + ref.SourceID,
			Metrics:  []string{"m1-" + ref.SourceID, "m2"},
			Href:     "/source/" + string(ref.SourceType) + "/" + ref.SourceID,
		}
	}
	return out, nil
}

// --- envelopes for assertions --------------------------------------------

type feedEnvelope struct {
	Message string       `json:"message"`
	Data    feedResponse `json:"data"`
}

type postDetailEnvelope struct {
	Message string             `json:"message"`
	Data    postDetailResponse `json:"data"`
}

type commentEnvelope struct {
	Message string     `json:"message"`
	Data    commentDTO `json:"data"`
}

type reactionsEnvelope struct {
	Message string       `json:"message"`
	Data    reactionsDTO `json:"data"`
}

// --- helpers -------------------------------------------------------------

func newTestHandler() (*Handler, *MemoryRepository, *fakeHydrator) {
	repo := NewMemoryRepository()
	hyd := &fakeHydrator{missing: map[string]bool{}}
	return NewHandler(repo, hyd), repo, hyd
}

// seedPost inserts a feed-index row for userID via the repo's EnsurePost and
// returns it.
func seedPost(t *testing.T, repo *MemoryRepository, userID, sourceID string, occurredAt time.Time) Post {
	t.Helper()
	p, err := repo.EnsurePost(context.Background(), PostRef{
		UserID:     userID,
		SourceType: SourceWorkout,
		SourceID:   sourceID,
		OccurredAt: occurredAt,
	})
	if err != nil {
		t.Fatalf("seed post %s: %v", sourceID, err)
	}
	return p
}

// req builds a request as the given user with optional chi URL params (passed
// as key,val pairs).
func req(t *testing.T, method, target, userID, body string, params ...string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rc := chi.NewRouteContext()
	for i := 0; i+1 < len(params); i += 2 {
		rc.URLParams.Add(params[i], params[i+1])
	}
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	if userID != "" {
		ctx = authctx.WithUserID(ctx, userID)
	}
	return r.WithContext(ctx)
}

// --- feed pagination + shape ---------------------------------------------

func TestFeedPaginationAndShape(t *testing.T) {
	h, repo, _ := newTestHandler()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Three posts, ascending occurred_at. Newest-first feed order: c, b, a.
	pa := seedPost(t, repo, userA, "a", base)
	_ = seedPost(t, repo, userA, "b", base.Add(time.Hour))
	pc := seedPost(t, repo, userA, "c", base.Add(2*time.Hour))

	// React + comment on the newest post (c) so the page decoration is checked.
	if _, err := repo.AddReaction(context.Background(), pc.ID, userA, ReactionLike); err != nil {
		t.Fatalf("seed reaction: %v", err)
	}
	if _, err := repo.AddComment(context.Background(), pc.ID, userA, "nice"); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	// Page 1: limit=2 → [c, b], non-nil next_before.
	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline?limit=2", userA, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("page1 status = %d; body=%s", w.Code, w.Body.String())
	}
	var p1 feedEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &p1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(p1.Data.Posts) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(p1.Data.Posts))
	}
	if p1.Data.Posts[0].ID != pc.ID {
		t.Errorf("page1[0] = %s, want newest %s", p1.Data.Posts[0].ID, pc.ID)
	}
	if p1.Data.NextBefore == nil {
		t.Fatal("expected non-nil next_before on full page")
	}

	// Assert the hydrated content + decoration on the newest post.
	top := p1.Data.Posts[0]
	if top.SourceType != SourceWorkout || top.SourceID != "c" {
		t.Errorf("top source = %s/%s, want workout/c", top.SourceType, top.SourceID)
	}
	if top.Visibility != VisibilityPrivate {
		t.Errorf("visibility = %q, want private", top.Visibility)
	}
	if top.Content.Title != "title-c" || top.Content.Href != "/source/workout/c" {
		t.Errorf("content wrong: %+v", top.Content)
	}
	if len(top.Content.Metrics) != 2 {
		t.Errorf("metrics = %v, want 2", top.Content.Metrics)
	}
	if top.Reactions.Summary[ReactionLike] != 1 {
		t.Errorf("summary[like] = %d, want 1", top.Reactions.Summary[ReactionLike])
	}
	if len(top.Reactions.Mine) != 1 || top.Reactions.Mine[0] != ReactionLike {
		t.Errorf("mine = %v, want [like]", top.Reactions.Mine)
	}
	if top.CommentCount != 1 {
		t.Errorf("comment_count = %d, want 1", top.CommentCount)
	}

	// Page 2: before=cursor → [a], next_before null at the end.
	w2 := httptest.NewRecorder()
	h.listFeed(w2, req(t, "GET", "/timeline?limit=2&before="+*p1.Data.NextBefore, userA, ""))
	if w2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d; body=%s", w2.Code, w2.Body.String())
	}
	var p2 feedEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &p2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(p2.Data.Posts) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(p2.Data.Posts))
	}
	if p2.Data.Posts[0].ID != pa.ID {
		t.Errorf("page2[0] = %s, want oldest %s", p2.Data.Posts[0].ID, pa.ID)
	}
	if p2.Data.NextBefore != nil {
		t.Errorf("expected nil next_before at end, got %q", *p2.Data.NextBefore)
	}
}

func TestFeedInvalidCursor(t *testing.T) {
	h, _, _ := newTestHandler()
	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline?before=not-a-valid-cursor!!", userA, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid cursor") {
		t.Errorf("body should mention invalid cursor, got %s", w.Body.String())
	}
}

func TestFeedMissingHydrationOmitted(t *testing.T) {
	h, repo, hyd := newTestHandler()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	keep := seedPost(t, repo, userA, "keep", base.Add(time.Hour))
	seedPost(t, repo, userA, "gone", base) // source deleted
	hyd.missing["gone"] = true

	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline", userA, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var env feedEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Posts) != 1 || env.Data.Posts[0].ID != keep.ID {
		t.Fatalf("expected only the hydrated post, got %+v", env.Data.Posts)
	}
}

func TestFeedScopedToViewer(t *testing.T) {
	h, repo, _ := newTestHandler()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seedPost(t, repo, userA, "a1", base)
	seedPost(t, repo, userB, "b1", base.Add(time.Hour)) // B's post must not appear in A's feed

	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline", userA, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var env feedEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Posts) != 1 || env.Data.Posts[0].SourceID != "a1" {
		t.Fatalf("A's feed should contain only A's posts, got %+v", env.Data.Posts)
	}
}

func TestFeedMissingUserContext(t *testing.T) {
	h, _, _ := newTestHandler()
	w := httptest.NewRecorder()
	// No authctx.WithUserID on the request.
	h.listFeed(w, req(t, "GET", "/timeline", "", ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// --- get post + comments thread ------------------------------------------

func TestGetPostWithComments(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	if _, err := repo.AddComment(context.Background(), p.ID, userA, "first"); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	w := httptest.NewRecorder()
	h.getPost(w, req(t, "GET", "/timeline/posts/"+p.ID, userA, "", "id", p.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var env postDetailEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ID != p.ID {
		t.Errorf("id = %s, want %s", env.Data.ID, p.ID)
	}
	if env.Data.Content.Title != "title-p" {
		t.Errorf("content not hydrated: %+v", env.Data.Content)
	}
	if len(env.Data.Comments) != 1 || env.Data.Comments[0].Body != "first" {
		t.Errorf("comments = %+v, want one 'first'", env.Data.Comments)
	}
}

func TestGetPostNotFound(t *testing.T) {
	h, _, _ := newTestHandler()
	w := httptest.NewRecorder()
	h.getPost(w, req(t, "GET", "/timeline/posts/nope", userA, "", "id", "nope"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- reactions: add/remove + stacking ------------------------------------

func TestReactionStackingAndRemoval(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())

	put := func(rt ReactionType) reactionsDTO {
		w := httptest.NewRecorder()
		h.addReaction(w, req(t, "PUT", "/timeline/posts/"+p.ID+"/reactions/"+string(rt), userA, "", "id", p.ID, "type", string(rt)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s status = %d, want 200; body=%s", rt, w.Code, w.Body.String())
		}
		var env reactionsEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode PUT %s: %v", rt, err)
		}
		return env.Data
	}

	// PUT like then PUT strong → both in summary, both in mine.
	put(ReactionLike)
	got := put(ReactionStrong)
	if got.Summary[ReactionLike] != 1 || got.Summary[ReactionStrong] != 1 {
		t.Errorf("summary after like+strong = %v, want like:1 strong:1", got.Summary)
	}
	if len(got.Mine) != 2 {
		t.Errorf("mine = %v, want 2 entries", got.Mine)
	}

	// PUT like again is idempotent — still one like.
	got = put(ReactionLike)
	if got.Summary[ReactionLike] != 1 {
		t.Errorf("summary[like] after repeat = %d, want 1", got.Summary[ReactionLike])
	}

	// DELETE like removes only like; strong remains.
	wd := httptest.NewRecorder()
	h.removeReaction(wd, req(t, "DELETE", "/timeline/posts/"+p.ID+"/reactions/like", userA, "", "id", p.ID, "type", "like"))
	if wd.Code != http.StatusNoContent {
		t.Fatalf("DELETE like status = %d, want 204", wd.Code)
	}
	summaries, err := repo.ReactionSummaries(context.Background(), []string{p.ID}, userA)
	if err != nil {
		t.Fatalf("summaries: %v", err)
	}
	s := summaries[p.ID]
	if _, ok := s.Counts[ReactionLike]; ok {
		t.Errorf("like should be gone, summary = %v", s.Counts)
	}
	if s.Counts[ReactionStrong] != 1 {
		t.Errorf("strong should remain, summary = %v", s.Counts)
	}
}

func TestReactionUnknownType(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	w := httptest.NewRecorder()
	h.addReaction(w, req(t, "PUT", "/timeline/posts/"+p.ID+"/reactions/bogus", userA, "", "id", p.ID, "type", "bogus"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- comments: create + delete -------------------------------------------

func TestAddCommentValidation(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())

	// Happy path → 201.
	w := httptest.NewRecorder()
	h.addComment(w, req(t, "POST", "/timeline/posts/"+p.ID+"/comments", userA, `{"body":"hello"}`, "id", p.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env commentEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ID == "" || env.Data.PostID != p.ID || env.Data.UserID != userA || env.Data.Body != "hello" {
		t.Errorf("comment dto wrong: %+v", env.Data)
	}

	// Empty + too-long → 400.
	for _, body := range []string{`{"body":"   "}`, `{"body":"` + strings.Repeat("a", 2001) + `"}`} {
		wb := httptest.NewRecorder()
		h.addComment(wb, req(t, "POST", "/timeline/posts/"+p.ID+"/comments", userA, body, "id", p.ID))
		if wb.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d, want 400", body, wb.Code)
		}
	}
}

func TestDeleteComment(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	c, err := repo.AddComment(context.Background(), p.ID, userA, "mine")
	if err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	// Owner deletes → 204.
	w := httptest.NewRecorder()
	h.deleteComment(w, req(t, "DELETE", "/timeline/posts/"+p.ID+"/comments/"+c.ID, userA, "", "id", p.ID, "commentId", c.ID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	// Gone from the live thread now.
	live, err := repo.ListComments(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("comment should be soft-deleted, got %d live", len(live))
	}
}

func TestDeleteOthersCommentForbidden(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	// A comments on A's own post; B tries to delete it.
	c, err := repo.AddComment(context.Background(), p.ID, userA, "A's comment")
	if err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	// But B can't even view A's post (canView), so B hits 404 at the post gate.
	w := httptest.NewRecorder()
	h.deleteComment(w, req(t, "DELETE", "/timeline/posts/"+p.ID+"/comments/"+c.ID, userB, "", "id", p.ID, "commentId", c.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("B deleting A's comment status = %d, want 404", w.Code)
	}
	// The comment must still be live.
	live, err := repo.ListComments(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(live) != 1 {
		t.Errorf("comment should survive B's delete attempt, got %d live", len(live))
	}
}

// TestModerateOwnershipMasking exercises canModerate directly: when a second
// user CAN view a post (future friends world) but does not own a comment, the
// delete is masked as 404. v1 has no such post, so the unit check stands in for
// the ownership branch the friends SOW relies on.
func TestModerateOwnershipMasking(t *testing.T) {
	c := Comment{UserID: userA}
	if canModerate(c, userB) {
		t.Error("canModerate should be false for a non-owner")
	}
	if !canModerate(c, userA) {
		t.Error("canModerate should be true for the owner")
	}
}

// --- authorization: second user locked out (critical) --------------------

func TestSecondUserCannotViewCommentOrReact(t *testing.T) {
	h, repo, _ := newTestHandler()
	p := seedPost(t, repo, userA, "secret", time.Now().UTC())

	// B GETs A's post → 404 (canView false, masked as not-found).
	wg := httptest.NewRecorder()
	h.getPost(wg, req(t, "GET", "/timeline/posts/"+p.ID, userB, "", "id", p.ID))
	if wg.Code != http.StatusNotFound {
		t.Errorf("B get A's post = %d, want 404", wg.Code)
	}

	// B POSTs a comment on A's post → 404.
	wc := httptest.NewRecorder()
	h.addComment(wc, req(t, "POST", "/timeline/posts/"+p.ID+"/comments", userB, `{"body":"hi"}`, "id", p.ID))
	if wc.Code != http.StatusNotFound {
		t.Errorf("B comment on A's post = %d, want 404", wc.Code)
	}

	// B PUTs a reaction on A's post → 404.
	wr := httptest.NewRecorder()
	h.addReaction(wr, req(t, "PUT", "/timeline/posts/"+p.ID+"/reactions/like", userB, "", "id", p.ID, "type", "like"))
	if wr.Code != http.StatusNotFound {
		t.Errorf("B react on A's post = %d, want 404", wr.Code)
	}

	// And none of B's attempts left a trace on A's post.
	live, _ := repo.ListComments(context.Background(), p.ID)
	if len(live) != 0 {
		t.Errorf("B's blocked comment leaked, got %d", len(live))
	}
	summaries, _ := repo.ReactionSummaries(context.Background(), []string{p.ID}, userB)
	if len(summaries) != 0 {
		t.Errorf("B's blocked reaction leaked, got %+v", summaries)
	}
}

// --- cursor codec round-trip ---------------------------------------------

func TestCursorRoundTrip(t *testing.T) {
	in := Cursor{OccurredAt: time.Date(2026, 6, 1, 12, 34, 56, 789, time.UTC), ID: "post-123"}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OccurredAt.Equal(in.OccurredAt) || out.ID != in.ID {
		t.Errorf("round-trip = %+v, want %+v", out, in)
	}
}
