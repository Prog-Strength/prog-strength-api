package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
)

// newDiscoveryFixture builds a memory user repo, a real memory follow repo (so
// relationships/counts are genuine), and a discovery handler mounted on a real
// chi router. Returns the router and both repos.
func newDiscoveryFixture(t *testing.T) (chi.Router, Repository, *follow.MemoryRepository) {
	t.Helper()
	userRepo := NewMemoryRepository()
	followRepo := follow.NewMemoryRepository()
	h := NewDiscoveryHandler(userRepo, followRepo, NewFakeAvatarStore(), &fakeLiftSource{}, &fakeRunSource{})
	r := chi.NewRouter()
	h.Mount(r)
	return r, userRepo, followRepo
}

// doAs issues a GET to path as viewer (auth context populated) against the
// router and returns the recorder.
func doAs(t *testing.T, r chi.Router, viewer, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), viewer))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decodeData unmarshals the httpresp envelope's data field into v.
func decodeData(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, w.Body.String())
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (data=%s)", err, env.Data)
	}
}

func setUsername(t *testing.T, repo Repository, u *User, username string) {
	t.Helper()
	u.Username = strPtr(username)
	if err := repo.Update(context.Background(), u); err != nil {
		t.Fatalf("set username %q: %v", username, err)
	}
}

// TestDiscovery_ProfileSelf verifies a user reading their own profile gets
// relationship self plus correct counts.
func TestDiscovery_ProfileSelf(t *testing.T) {
	r, userRepo, followRepo := newDiscoveryFixture(t)
	ctx := context.Background()

	me := makeUser(t, userRepo, "me@example.com")
	setUsername(t, userRepo, me, "me_handle")
	other := makeUser(t, userRepo, "o@example.com")
	setUsername(t, userRepo, other, "other_handle")

	// other follows me (accepted) → me has 1 follower.
	if _, err := followRepo.Request(ctx, other.ID, me.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := followRepo.Accept(ctx, me.ID, other.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	w := doAs(t, r, me.ID, "/users/me_handle")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var p publicProfileDTO
	decodeData(t, w, &p)
	if p.Relationship != follow.RelationshipSelf {
		t.Fatalf("relationship = %s, want self", p.Relationship)
	}
	if p.FollowerCount != 1 {
		t.Fatalf("follower_count = %d, want 1", p.FollowerCount)
	}
	if p.FollowingCount != 0 {
		t.Fatalf("following_count = %d, want 0", p.FollowingCount)
	}
	if p.UserID != me.ID {
		t.Fatalf("user_id = %s, want %s", p.UserID, me.ID)
	}
}

// TestDiscovery_ProfileRelationshipFollowing verifies a viewer who follows the
// target sees relationship following; a non-related viewer sees none.
func TestDiscovery_ProfileRelationship(t *testing.T) {
	r, userRepo, followRepo := newDiscoveryFixture(t)
	ctx := context.Background()

	viewer := makeUser(t, userRepo, "v@example.com")
	setUsername(t, userRepo, viewer, "viewer")
	target := makeUser(t, userRepo, "t@example.com")
	setUsername(t, userRepo, target, "target")
	stranger := makeUser(t, userRepo, "s@example.com")
	setUsername(t, userRepo, stranger, "stranger")

	// viewer → target accepted.
	if _, err := followRepo.Request(ctx, viewer.ID, target.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := followRepo.Accept(ctx, target.ID, viewer.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// viewer sees "following".
	w := doAs(t, r, viewer.ID, "/users/target")
	var p publicProfileDTO
	decodeData(t, w, &p)
	if p.Relationship != follow.RelationshipFollowing {
		t.Fatalf("viewer relationship = %s, want following", p.Relationship)
	}
	if p.FollowerCount != 1 {
		t.Fatalf("target follower_count = %d, want 1", p.FollowerCount)
	}

	// stranger sees "none".
	w = doAs(t, r, stranger.ID, "/users/target")
	decodeData(t, w, &p)
	if p.Relationship != follow.RelationshipNone {
		t.Fatalf("stranger relationship = %s, want none", p.Relationship)
	}
}

// TestDiscovery_ProfileBio verifies the public profile carries the bio when set
// and null when unset.
func TestDiscovery_ProfileBio(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)
	ctx := context.Background()

	withBio := makeUser(t, userRepo, "bio@example.com")
	withBio.Username = strPtr("with_bio")
	withBio.Bio = strPtr("squat enthusiast")
	if err := userRepo.Update(ctx, withBio); err != nil {
		t.Fatalf("set bio: %v", err)
	}
	noBio := makeUser(t, userRepo, "nobio@example.com")
	setUsername(t, userRepo, noBio, "no_bio")

	w := doAs(t, r, withBio.ID, "/users/with_bio")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var p publicProfileDTO
	decodeData(t, w, &p)
	if p.Bio == nil || *p.Bio != "squat enthusiast" {
		t.Fatalf("bio = %v, want set", p.Bio)
	}

	w = doAs(t, r, withBio.ID, "/users/no_bio")
	decodeData(t, w, &p)
	if p.Bio != nil {
		t.Fatalf("bio = %v, want nil", *p.Bio)
	}
}

// TestDiscovery_ProfileUnknownUsername verifies an unknown handle 404s.
func TestDiscovery_ProfileUnknownUsername(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)
	me := makeUser(t, userRepo, "me@example.com")
	w := doAs(t, r, me.ID, "/users/ghost")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
}

// TestDiscovery_FollowersList verifies the followers list carries the right rows
// and the viewer's relationship to each.
func TestDiscovery_FollowersList(t *testing.T) {
	r, userRepo, followRepo := newDiscoveryFixture(t)
	ctx := context.Background()

	owner := makeUser(t, userRepo, "owner@example.com")
	setUsername(t, userRepo, owner, "owner")
	f1 := makeUser(t, userRepo, "f1@example.com")
	setUsername(t, userRepo, f1, "follower_one")
	f2 := makeUser(t, userRepo, "f2@example.com")
	setUsername(t, userRepo, f2, "follower_two")
	viewer := makeUser(t, userRepo, "viewer@example.com")
	setUsername(t, userRepo, viewer, "viewer")

	// f1 and f2 follow owner (accepted).
	for _, f := range []*User{f1, f2} {
		if _, err := followRepo.Request(ctx, f.ID, owner.ID); err != nil {
			t.Fatalf("request: %v", err)
		}
		if err := followRepo.Accept(ctx, owner.ID, f.ID); err != nil {
			t.Fatalf("accept: %v", err)
		}
	}
	// viewer → f1 accepted, so viewer's relationship to f1 is "following".
	if _, err := followRepo.Request(ctx, viewer.ID, f1.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := followRepo.Accept(ctx, f1.ID, viewer.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	w := doAs(t, r, viewer.ID, "/users/owner/followers")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp listResponse
	decodeData(t, w, &resp)
	if len(resp.Users) != 2 {
		t.Fatalf("got %d follower rows, want 2", len(resp.Users))
	}
	rels := map[string]follow.Relationship{}
	for _, u := range resp.Users {
		rels[u.UserID] = u.Relationship
	}
	if rels[f1.ID] != follow.RelationshipFollowing {
		t.Fatalf("viewer→f1 = %s, want following", rels[f1.ID])
	}
	if rels[f2.ID] != follow.RelationshipNone {
		t.Fatalf("viewer→f2 = %s, want none", rels[f2.ID])
	}
}

// TestDiscovery_FollowersPagination walks the followers list with limit=1 and
// confirms cursor round-trips produce every row exactly once.
func TestDiscovery_FollowersPagination(t *testing.T) {
	r, userRepo, followRepo := newDiscoveryFixture(t)
	ctx := context.Background()

	owner := makeUser(t, userRepo, "owner@example.com")
	setUsername(t, userRepo, owner, "owner")
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		f := makeUser(t, userRepo, string(rune('a'+i))+"@example.com")
		if _, err := followRepo.Request(ctx, f.ID, owner.ID); err != nil {
			t.Fatalf("request: %v", err)
		}
		if err := followRepo.Accept(ctx, owner.ID, f.ID); err != nil {
			t.Fatalf("accept: %v", err)
		}
		want[f.ID] = true
	}

	got := map[string]bool{}
	path := "/users/owner/followers?limit=1"
	for {
		w := doAs(t, r, owner.ID, path)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
		}
		var resp listResponse
		decodeData(t, w, &resp)
		for _, u := range resp.Users {
			if got[u.UserID] {
				t.Fatalf("duplicate row %s", u.UserID)
			}
			got[u.UserID] = true
		}
		if resp.NextCursor == nil {
			break
		}
		path = "/users/owner/followers?limit=1&cursor=" + *resp.NextCursor
		if len(got) > len(want) {
			t.Fatalf("pagination overran: %v", got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("missing follower %s", id)
		}
	}
}

// TestDiscovery_FollowingList verifies the following list uses the FolloweeID
// side of each edge.
func TestDiscovery_FollowingList(t *testing.T) {
	r, userRepo, followRepo := newDiscoveryFixture(t)
	ctx := context.Background()

	owner := makeUser(t, userRepo, "owner@example.com")
	setUsername(t, userRepo, owner, "owner")
	followee := makeUser(t, userRepo, "fe@example.com")
	setUsername(t, userRepo, followee, "followee")

	// owner → followee accepted: followee appears in owner's "following".
	if _, err := followRepo.Request(ctx, owner.ID, followee.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := followRepo.Accept(ctx, followee.ID, owner.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	w := doAs(t, r, owner.ID, "/users/owner/following")
	var resp listResponse
	decodeData(t, w, &resp)
	if len(resp.Users) != 1 || resp.Users[0].UserID != followee.ID {
		t.Fatalf("following = %+v, want [%s]", resp.Users, followee.ID)
	}
	// owner → followee accepted → relationship following.
	if resp.Users[0].Relationship != follow.RelationshipFollowing {
		t.Fatalf("relationship = %s, want following", resp.Users[0].Relationship)
	}
}

// TestDiscovery_ListUnknownUsername verifies an unknown handle on a list 404s.
func TestDiscovery_ListUnknownUsername(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)
	me := makeUser(t, userRepo, "me@example.com")
	w := doAs(t, r, me.ID, "/users/ghost/followers")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestDiscovery_Search verifies ranked search results including the searcher's
// own self row.
func TestDiscovery_Search(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)

	searcher := makeUser(t, userRepo, "searcher@example.com")
	setUsername(t, userRepo, searcher, "jim")        // exact match for "jim"
	prefix := makeUser(t, userRepo, "p@example.com") //
	setUsername(t, userRepo, prefix, "jimmy")        // prefix match
	makeNamedUser(t, userRepo, "n@example.com", "Nope", strPtr("zzz"))

	// searcher searches "jim" — their own exact-username row is included with
	// relationship self.
	w := doAs(t, r, searcher.ID, "/users/search?q=jim")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	var resp listResponse
	decodeData(t, w, &resp)
	if len(resp.Users) != 2 {
		t.Fatalf("got %d results, want 2 (%+v)", len(resp.Users), resp.Users)
	}
	if resp.Users[0].UserID != searcher.ID {
		t.Fatalf("first result = %s, want exact-match searcher %s", resp.Users[0].UserID, searcher.ID)
	}
	if resp.Users[0].Relationship != follow.RelationshipSelf {
		t.Fatalf("searcher self row relationship = %s, want self", resp.Users[0].Relationship)
	}
	if resp.Users[1].UserID != prefix.ID {
		t.Fatalf("second result = %s, want prefix %s", resp.Users[1].UserID, prefix.ID)
	}
}

// TestDiscovery_SearchInvalidCursor verifies a malformed cursor is a 400.
func TestDiscovery_SearchInvalidCursor(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)
	me := makeUser(t, userRepo, "me@example.com")
	w := doAs(t, r, me.ID, "/users/search?q=jim&cursor=!!!not-base64!!!")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

// TestDiscovery_SearchNotShadowedByProfile is the route-precedence guard: the
// static /users/search must win over /users/{username} when both are mounted on
// the same router, and /users/{username} must still resolve a real handle.
func TestDiscovery_SearchNotShadowedByProfile(t *testing.T) {
	r, userRepo, _ := newDiscoveryFixture(t)
	me := makeUser(t, userRepo, "me@example.com")
	setUsername(t, userRepo, me, "me_handle")

	// /users/search resolves to the search handler (200 + a users list),
	// NOT the profile handler (which would 404 on a "search" username).
	w := doAs(t, r, me.ID, "/users/search?q=me")
	if w.Code != http.StatusOK {
		t.Fatalf("/users/search status = %d, want 200 (search not shadowed) body=%s", w.Code, w.Body.String())
	}
	var resp listResponse
	decodeData(t, w, &resp)
	if len(resp.Users) == 0 {
		t.Fatalf("/users/search returned no users — likely routed to profile handler")
	}

	// /users/{username} still works for a real handle.
	w = doAs(t, r, me.ID, "/users/me_handle")
	if w.Code != http.StatusOK {
		t.Fatalf("/users/me_handle status = %d, want 200", w.Code)
	}
	var p publicProfileDTO
	decodeData(t, w, &p)
	if p.UserID != me.ID {
		t.Fatalf("profile user_id = %s, want %s", p.UserID, me.ID)
	}
}

// TestDiscovery_SearchCursorCodec round-trips a SearchCursor whose sort key
// contains the delimiter and base64 padding edge bytes, proving the
// length-prefixed framing survives.
func TestDiscovery_SearchCursorCodec(t *testing.T) {
	cases := []SearchCursor{
		{Bucket: 0, SortKey: "plain", ID: "abc123"},
		{Bucket: 2, SortKey: "weird|key|with|pipes", ID: "id_99"},
		{Bucket: 1, SortKey: "", ID: "only-id"},
	}
	for _, c := range cases {
		got, err := decodeSearchCursor(encodeSearchCursor(c))
		if err != nil {
			t.Fatalf("decode(%+v): %v", c, err)
		}
		if got != c {
			t.Fatalf("round-trip = %+v, want %+v", got, c)
		}
	}
}
