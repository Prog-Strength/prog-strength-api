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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
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

// --- fake follow/user seams ----------------------------------------------

// fakeFollowees is a deterministic in-test AcceptedFollowees. accepted maps a
// viewer id to the set of user ids whose non-private posts that viewer may see.
type fakeFollowees struct {
	accepted map[string][]string
}

func (f *fakeFollowees) AcceptedFollowees(ctx context.Context, viewerID string) ([]string, error) {
	return f.accepted[viewerID], nil
}

// fakeUsers is a deterministic in-test UserResolver. byUsername maps a username
// to a user id; an unknown username returns ErrNotFound so the handler's
// not-found branch fires.
type fakeUsers struct {
	byUsername map[string]string
}

func (f *fakeUsers) ResolveUsername(ctx context.Context, username string) (string, error) {
	id, ok := f.byUsername[username]
	if !ok {
		return "", ErrNotFound
	}
	return id, nil
}

// --- fake profile resolver -----------------------------------------------

// fakeProfiles is a deterministic in-test ProfileResolver. authors maps a user
// id to its resolved Author; an id absent from the map is absent from the
// returned result (the missing-author path). It records each call's input ids
// and counts invocations so a test can assert the batch contract — one call per
// page over the deduped id set, never an N+1.
type fakeProfiles struct {
	authors map[string]Author
	calls   int
	lastIDs []string
}

func (f *fakeProfiles) Authors(ctx context.Context, userIDs []string) (map[string]Author, error) {
	f.calls++
	f.lastIDs = append([]string(nil), userIDs...)
	out := make(map[string]Author, len(userIDs))
	for _, id := range userIDs {
		if a, ok := f.authors[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

// strptr is a small helper for the optional *string author fields.
func strptr(s string) *string { return &s }

// authorFor builds a deterministic Author for a user id so tests can pin exact
// values against the fake's data.
func authorFor(userID string) Author {
	return Author{
		UserID:      userID,
		Username:    strptr("u-" + userID),
		DisplayName: "Display " + userID,
		AvatarURL:   strptr("https://avatars.test/" + userID),
	}
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

func newTestHandler(t *testing.T) (*Handler, *SQLiteRepository, *fakeHydrator) {
	h, repo, hyd, _, _, _ := newSocialTestHandler(t)
	return h, repo, hyd
}

// newSocialTestHandler additionally exposes the follow/user fakes for the
// fan-out and ?user= scoped-feed tests, plus the profile resolver fake for the
// author-embedding tests. The fakes start empty (no followees, no resolvable
// usernames); the profile resolver is pre-seeded with authors for userA/userB
// so the common posts have a resolvable identity. Tests populate/override as
// needed.
func newSocialTestHandler(t *testing.T) (*Handler, *SQLiteRepository, *fakeHydrator, *fakeFollowees, *fakeUsers, *fakeProfiles) {
	repo := NewSQLiteRepository(dbtest.New(t))
	hyd := &fakeHydrator{missing: map[string]bool{}}
	followees := &fakeFollowees{accepted: map[string][]string{}}
	users := &fakeUsers{byUsername: map[string]string{}}
	profiles := &fakeProfiles{authors: map[string]Author{
		userA: authorFor(userA),
		userB: authorFor(userB),
	}}
	return NewHandler(repo, hyd, followees, users, profiles), repo, hyd, followees, users, profiles
}

// seedPost inserts a feed-index row for userID via the repo's EnsurePost and
// returns it.
func seedPost(t *testing.T, repo *SQLiteRepository, userID, sourceID string, occurredAt time.Time) Post {
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

// seedPostVis inserts a post for userID and forces its visibility (EnsurePost
// always writes 'friends'), letting a test pin a 'private' or 'public' post.
// EnsurePost has no visibility argument, so the override is a direct UPDATE on
// the shared test DB — the same backdoor the SQLite repo test uses to exercise
// the per-post visibility fan-out.
func seedPostVis(t *testing.T, repo *SQLiteRepository, userID, sourceID string, occurredAt time.Time, vis Visibility) Post {
	t.Helper()
	p := seedPost(t, repo, userID, sourceID, occurredAt)
	if _, err := repo.db.ExecContext(context.Background(),
		`UPDATE timeline_post SET visibility = ? WHERE id = ?`, string(vis), p.ID); err != nil {
		t.Fatalf("set visibility for %s: %v", sourceID, err)
	}
	p.Visibility = vis
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
	h, repo, _ := newTestHandler(t)
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
	if top.Visibility != VisibilityFriends {
		t.Errorf("visibility = %q, want friends", top.Visibility)
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
	h, _, _ := newTestHandler(t)
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
	h, repo, hyd := newTestHandler(t)
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
	h, repo, _ := newTestHandler(t)
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
	h, _, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	// No authctx.WithUserID on the request.
	h.listFeed(w, req(t, "GET", "/timeline", "", ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// --- get post + comments thread ------------------------------------------

func TestGetPostWithComments(t *testing.T) {
	h, repo, _ := newTestHandler(t)
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
	h, _, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	h.getPost(w, req(t, "GET", "/timeline/posts/nope", userA, "", "id", "nope"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- reactions: add/remove + stacking ------------------------------------

func TestReactionStackingAndRemoval(t *testing.T) {
	h, repo, _ := newTestHandler(t)
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
	h, repo, _ := newTestHandler(t)
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	w := httptest.NewRecorder()
	h.addReaction(w, req(t, "PUT", "/timeline/posts/"+p.ID+"/reactions/bogus", userA, "", "id", p.ID, "type", "bogus"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- comments: create + delete -------------------------------------------

func TestAddCommentValidation(t *testing.T) {
	h, repo, _ := newTestHandler(t)
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
	h, repo, _ := newTestHandler(t)
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
	h, repo, _ := newTestHandler(t)
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
	h, repo, _ := newTestHandler(t)
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

// --- timeline fan-out: the multi-author home feed ------------------------

// fetchFeed runs GET /timeline (optional raw query like "?user=bob") as viewer
// and returns the decoded payload.
func fetchFeed(t *testing.T, h *Handler, viewer, rawQuery string) feedResponse {
	t.Helper()
	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline"+rawQuery, viewer, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("listFeed status = %d; body=%s", w.Code, w.Body.String())
	}
	var env feedEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	return env.Data
}

func sourceIDSet(posts []postDTO) map[string]bool {
	s := make(map[string]bool, len(posts))
	for _, p := range posts {
		s[p.SourceID] = true
	}
	return s
}

// An accepted follower sees the followee's friends posts and NOT their private
// posts; own-post visibility is unchanged (the viewer sees their own private +
// friends). Removing the edge immediately revokes visibility.
func TestFeedFanOutAcceptedFollower(t *testing.T) {
	h, repo, _, followees, _, _ := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// B's posts: one friends (visible to an accepted follower), one private (not).
	seedPostVis(t, repo, userB, "b-friends", base.Add(time.Hour), VisibilityFriends)
	seedPostVis(t, repo, userB, "b-private", base.Add(2*time.Hour), VisibilityPrivate)
	// A's own posts: a private one A must still see in their own feed.
	seedPostVis(t, repo, userA, "a-private", base, VisibilityPrivate)

	// A follows B (accepted).
	followees.accepted[userA] = []string{userB}

	got := sourceIDSet(fetchFeed(t, h, userA, "").Posts)
	if !got["b-friends"] {
		t.Errorf("accepted follower should see followee's friends post; got %v", got)
	}
	if got["b-private"] {
		t.Errorf("accepted follower must NOT see followee's private post; got %v", got)
	}
	if !got["a-private"] {
		t.Errorf("viewer should see their own private post; got %v", got)
	}

	// Unfollow / remove: empty accepted set immediately revokes B's posts.
	followees.accepted[userA] = nil
	got2 := sourceIDSet(fetchFeed(t, h, userA, "").Posts)
	if got2["b-friends"] || got2["b-private"] {
		t.Errorf("after unfollow, none of B's posts should appear; got %v", got2)
	}
	if !got2["a-private"] {
		t.Errorf("own posts must survive unfollow; got %v", got2)
	}
}

// A non-accepted (e.g. pending) follower has an empty accepted set, so they see
// nothing of the other author.
func TestFeedFanOutPendingSeesNothing(t *testing.T) {
	h, repo, _, _, _, _ := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seedPostVis(t, repo, userB, "b-friends", base, VisibilityFriends)
	// followees.accepted[userA] left empty — A's request to B is pending.

	got := sourceIDSet(fetchFeed(t, h, userA, "").Posts)
	if len(got) != 0 {
		t.Errorf("pending follower should see no posts of B; got %v", got)
	}
}

// The multi-author keyset page orders correctly across authors with interleaved
// occurred_at, paginating without gaps or repeats.
func TestFeedFanOutKeysetAcrossAuthors(t *testing.T) {
	h, repo, _, followees, _, _ := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Interleave A and B by occurred_at: a0 < b1 < a2 < b3 (all friends/own).
	seedPostVis(t, repo, userA, "a0", base, VisibilityFriends)
	seedPostVis(t, repo, userB, "b1", base.Add(1*time.Hour), VisibilityFriends)
	seedPostVis(t, repo, userA, "a2", base.Add(2*time.Hour), VisibilityFriends)
	seedPostVis(t, repo, userB, "b3", base.Add(3*time.Hour), VisibilityFriends)
	followees.accepted[userA] = []string{userB}

	// Walk in pages of 2 and collect the source-id order.
	var order []string
	rawQuery := "?limit=2"
	for {
		page := fetchFeed(t, h, userA, rawQuery)
		for _, p := range page.Posts {
			order = append(order, p.SourceID)
		}
		if page.NextBefore == nil {
			break
		}
		rawQuery = "?limit=2&before=" + *page.NextBefore
		if len(order) > 4 {
			t.Fatal("pagination did not terminate")
		}
	}
	want := []string{"b3", "a2", "b1", "a0"} // newest-first across authors
	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("page order = %v, want %v", order, want)
		}
	}
}

// --- canView via getPost (single post) -----------------------------------

func TestGetPostFollowerVisibility(t *testing.T) {
	h, repo, _, followees, _, _ := newSocialTestHandler(t)
	now := time.Now().UTC()
	friends := seedPostVis(t, repo, userA, "a-friends", now, VisibilityFriends)
	private := seedPostVis(t, repo, userA, "a-private", now, VisibilityPrivate)

	// B follows A (accepted): can GET A's friends post, but not the private one.
	followees.accepted[userB] = []string{userA}

	get := func(viewer, postID string) int {
		w := httptest.NewRecorder()
		h.getPost(w, req(t, "GET", "/timeline/posts/"+postID, viewer, "", "id", postID))
		return w.Code
	}

	if code := get(userB, friends.ID); code != http.StatusOK {
		t.Errorf("accepted follower GET friends post = %d, want 200", code)
	}
	if code := get(userB, private.ID); code != http.StatusNotFound {
		t.Errorf("accepted follower GET private post = %d, want 404", code)
	}
	// Author sees their own private post.
	if code := get(userA, private.ID); code != http.StatusOK {
		t.Errorf("author GET own private post = %d, want 200", code)
	}

	// A non-follower (C) sees neither.
	if code := get("userC", friends.ID); code != http.StatusNotFound {
		t.Errorf("non-follower GET friends post = %d, want 404", code)
	}
}

// --- ?user= scoped feed --------------------------------------------------

func TestScopedFeedAuthorSeesOwnIncludingPrivate(t *testing.T) {
	h, repo, _, _, users, _ := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seedPostVis(t, repo, userA, "a-friends", base, VisibilityFriends)
	seedPostVis(t, repo, userA, "a-private", base.Add(time.Hour), VisibilityPrivate)
	users.byUsername["alice"] = userA

	data := fetchFeed(t, h, userA, "?user=alice")
	got := sourceIDSet(data.Posts)
	if !got["a-friends"] || !got["a-private"] {
		t.Errorf("author's scoped feed should include own private + friends; got %v", got)
	}
	if data.Locked {
		t.Error("author's own scoped feed must not be locked")
	}
}

func TestScopedFeedAcceptedFollowerSeesFriendsOnly(t *testing.T) {
	h, repo, _, followees, users, _ := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seedPostVis(t, repo, userA, "a-friends", base, VisibilityFriends)
	seedPostVis(t, repo, userA, "a-private", base.Add(time.Hour), VisibilityPrivate)
	users.byUsername["alice"] = userA
	followees.accepted[userB] = []string{userA}

	data := fetchFeed(t, h, userB, "?user=alice")
	got := sourceIDSet(data.Posts)
	if !got["a-friends"] {
		t.Errorf("accepted follower scoped feed should include friends post; got %v", got)
	}
	if got["a-private"] {
		t.Errorf("accepted follower scoped feed must exclude private post; got %v", got)
	}
	if data.Locked {
		t.Error("accepted follower scoped feed must not be locked")
	}
}

func TestScopedFeedNonFollowerLocked(t *testing.T) {
	h, repo, _, _, users, _ := newSocialTestHandler(t)
	seedPostVis(t, repo, userA, "a-friends", time.Now().UTC(), VisibilityFriends)
	users.byUsername["alice"] = userA
	// userB does not follow A → gated locked-empty 200.

	data := fetchFeed(t, h, userB, "?user=alice")
	if !data.Locked {
		t.Error("non-follower scoped feed should be locked")
	}
	if len(data.Posts) != 0 {
		t.Errorf("locked scoped feed must be empty; got %v", data.Posts)
	}
	if data.NextBefore != nil {
		t.Errorf("locked scoped feed next_before must be nil; got %q", *data.NextBefore)
	}
}

func TestScopedFeedUnknownUsername(t *testing.T) {
	h, _, _, _, _, _ := newSocialTestHandler(t)
	w := httptest.NewRecorder()
	h.listFeed(w, req(t, "GET", "/timeline?user=ghost", userA, ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown username status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// --- embedded author identity --------------------------------------------

// Every feed post carries a populated author matching the resolver's data, and
// the resolver is invoked exactly ONCE for the page over the DEDUPED author id
// set — the N+1 guard. Two posts by userA + one by userB (an accepted followee)
// must dedupe to {userA, userB} in a single resolver call.
func TestFeedEmbedsAuthorBatchedOnce(t *testing.T) {
	h, repo, _, followees, _, profiles := newSocialTestHandler(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seedPostVis(t, repo, userA, "a1", base, VisibilityFriends)
	seedPostVis(t, repo, userA, "a2", base.Add(time.Hour), VisibilityFriends)
	seedPostVis(t, repo, userB, "b1", base.Add(2*time.Hour), VisibilityFriends)
	followees.accepted[userA] = []string{userB}

	data := fetchFeed(t, h, userA, "")
	if len(data.Posts) != 3 {
		t.Fatalf("want 3 posts, got %d", len(data.Posts))
	}
	for _, p := range data.Posts {
		var owner string
		switch p.SourceID {
		case "a1", "a2":
			owner = userA
		case "b1":
			owner = userB
		}
		exp := authorFor(owner)
		if p.Author.UserID != owner {
			t.Errorf("post %s author.user_id = %q, want %q", p.SourceID, p.Author.UserID, owner)
		}
		if p.Author.DisplayName != exp.DisplayName {
			t.Errorf("post %s author.display_name = %q, want %q", p.SourceID, p.Author.DisplayName, exp.DisplayName)
		}
		if p.Author.AvatarURL == nil || *p.Author.AvatarURL != *exp.AvatarURL {
			t.Errorf("post %s author.avatar_url = %v, want %q", p.SourceID, p.Author.AvatarURL, *exp.AvatarURL)
		}
		if p.Author.Username == nil || *p.Author.Username != *exp.Username {
			t.Errorf("post %s author.username = %v, want %q", p.SourceID, p.Author.Username, *exp.Username)
		}
	}

	// N+1 guard: one resolver call for the whole page, with the deduped id set.
	if profiles.calls != 1 {
		t.Fatalf("resolver invoked %d times, want exactly 1 (no N+1)", profiles.calls)
	}
	got := map[string]int{}
	for _, id := range profiles.lastIDs {
		got[id]++
	}
	if len(profiles.lastIDs) != 2 || got[userA] != 1 || got[userB] != 1 {
		t.Errorf("resolver ids = %v, want deduped {userA, userB}", profiles.lastIDs)
	}
}

// A single post detail embeds the post author AND every comment author, with
// commenters resolved in ONE batch call. Two comments from the same user dedupe
// to a single id in that call.
func TestGetPostEmbedsCommentAuthorsBatchedOnce(t *testing.T) {
	h, repo, _, _, _, profiles := newSocialTestHandler(t)
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	// Two comments from userA (dedupe) + one from userB.
	if _, err := repo.AddComment(context.Background(), p.ID, userA, "first"); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if _, err := repo.AddComment(context.Background(), p.ID, userA, "second"); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if _, err := repo.AddComment(context.Background(), p.ID, userB, "third"); err != nil {
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

	// Post author embedded.
	if env.Data.Author.UserID != userA || env.Data.Author.DisplayName != authorFor(userA).DisplayName {
		t.Errorf("post author = %+v, want userA's", env.Data.Author)
	}
	// Every comment has an author matching its commenter.
	if len(env.Data.Comments) != 3 {
		t.Fatalf("want 3 comments, got %d", len(env.Data.Comments))
	}
	for _, c := range env.Data.Comments {
		exp := authorFor(c.UserID)
		if c.Author.UserID != c.UserID {
			t.Errorf("comment %s author.user_id = %q, want %q", c.ID, c.Author.UserID, c.UserID)
		}
		if c.Author.DisplayName != exp.DisplayName {
			t.Errorf("comment %s author.display_name = %q, want %q", c.ID, c.Author.DisplayName, exp.DisplayName)
		}
		// Delete affordance: the raw user_id must still be present.
		if c.UserID == "" {
			t.Errorf("comment %s lost its user_id (delete affordance)", c.ID)
		}
	}

	// One call for the post (assemblePosts) + one for the comment thread.
	if profiles.calls != 2 {
		t.Fatalf("resolver invoked %d times, want 2 (one post page + one comment batch)", profiles.calls)
	}
	// The comment batch (last call) deduped userA's two comments to a single id.
	got := map[string]int{}
	for _, id := range profiles.lastIDs {
		got[id]++
	}
	if len(profiles.lastIDs) != 2 || got[userA] != 1 || got[userB] != 1 {
		t.Errorf("comment resolver ids = %v, want deduped {userA, userB}", profiles.lastIDs)
	}
}

// A missing author (resolver returns no entry for the post's owner) renders the
// post with a minimal author carrying just the UserID — the post is NOT dropped,
// and there is no panic.
func TestFeedMissingAuthorRendersMinimal(t *testing.T) {
	h, repo, _, _, _, profiles := newSocialTestHandler(t)
	// Resolver knows nobody.
	profiles.authors = map[string]Author{}
	seedPost(t, repo, userA, "orphan", time.Now().UTC())

	data := fetchFeed(t, h, userA, "")
	if len(data.Posts) != 1 {
		t.Fatalf("missing author must not drop the post; got %d posts", len(data.Posts))
	}
	a := data.Posts[0].Author
	if a.UserID != userA {
		t.Errorf("minimal author.user_id = %q, want %q", a.UserID, userA)
	}
	if a.DisplayName != "" || a.Username != nil || a.AvatarURL != nil {
		t.Errorf("minimal author should carry only the user id, got %+v", a)
	}
}

// The comment's user_id field survives author embedding (the delete affordance
// depends on it), even when its author resolves successfully.
func TestCommentRetainsUserIDForDeleteAffordance(t *testing.T) {
	h, repo, _, _, _, _ := newSocialTestHandler(t)
	p := seedPost(t, repo, userA, "p", time.Now().UTC())
	if _, err := repo.AddComment(context.Background(), p.ID, userA, "mine"); err != nil {
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
	if len(env.Data.Comments) != 1 || env.Data.Comments[0].UserID != userA {
		t.Fatalf("comment user_id must remain %q, got %+v", userA, env.Data.Comments)
	}
}
