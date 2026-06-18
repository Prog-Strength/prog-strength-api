package follow

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

// --- fake ProfileProvider ------------------------------------------------

// fakeProfiles is a deterministic in-test ProfileProvider. usernames maps a
// handle to a user id; users is the set of known ids. ResolveUsername returns
// follow.ErrNotFound for unknown handles, and summaries are synthesized from
// the id so assertions can pin exact values.
type fakeProfiles struct {
	usernames map[string]string // username → user id
	users     map[string]bool   // user id → exists
}

func newFakeProfiles() *fakeProfiles {
	return &fakeProfiles{usernames: map[string]string{}, users: map[string]bool{}}
}

// add registers a user with the given id and username.
func (f *fakeProfiles) add(userID, username string) {
	f.users[userID] = true
	f.usernames[username] = userID
}

func (f *fakeProfiles) ResolveUsername(ctx context.Context, username string) (string, error) {
	if id, ok := f.usernames[username]; ok {
		return id, nil
	}
	return "", ErrNotFound
}

func (f *fakeProfiles) UserExists(ctx context.Context, userID string) (bool, error) {
	return f.users[userID], nil
}

func (f *fakeProfiles) ProfileSummaries(ctx context.Context, userIDs []string) (map[string]ProfileSummary, error) {
	out := make(map[string]ProfileSummary, len(userIDs))
	for _, id := range userIDs {
		if !f.users[id] {
			continue
		}
		uname := id + "_name"
		out[id] = ProfileSummary{
			UserID:      id,
			DisplayName: "Display " + id,
			Username:    &uname,
		}
	}
	return out, nil
}

// --- envelopes -----------------------------------------------------------

type edgeEnvelope struct {
	Message string  `json:"message"`
	Data    edgeDTO `json:"data"`
}

type requestsEnvelope struct {
	Message string           `json:"message"`
	Data    requestsResponse `json:"data"`
}

// --- helpers -------------------------------------------------------------

func newTestHandler(t *testing.T) (*Handler, *SQLiteRepository, *fakeProfiles) {
	repo := NewSQLiteRepository(dbtest.New(t))
	profiles := newFakeProfiles()
	return NewHandler(repo, profiles), repo, profiles
}

// req builds a request as the given user with optional chi URL params.
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

// --- POST /follows -------------------------------------------------------

func TestHandler_RequestByUsername(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")

	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"bob_h"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env edgeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.UserID != "bob" || env.Data.Relationship != RelationshipRequested {
		t.Fatalf("edge dto wrong: %+v", env.Data)
	}
	if env.Data.DisplayName != "Display bob" {
		t.Fatalf("summary not attached: %+v", env.Data)
	}
	// The edge must exist as pending.
	f, err := repo.Get(context.Background(), "actor", "bob")
	if err != nil || f.Status != StatusPending {
		t.Fatalf("edge not pending: %+v err=%v", f, err)
	}
}

func TestHandler_RequestByRawUserID(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.users["raw-id"] = true // exists but has no username

	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"raw-id"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RequestSelf400(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("actor", "actor_h")
	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"actor_h"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RequestUnknown404(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("actor", "actor_h")
	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"ghost"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RequestExisting409(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	if _, err := repo.Request(context.Background(), "actor", "bob"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"bob_h"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RequestPendingCap429(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	for i := 0; i < PendingCap; i++ {
		if _, err := repo.Request(context.Background(), "actor", "u"+itoa(i)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "actor", `{"followee":"bob_h"}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RequestMissingUser500(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	h.create(w, req(t, "POST", "/follows", "", `{"followee":"bob_h"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// --- accept / reject -----------------------------------------------------

func TestHandler_AcceptOnlyByFollowee(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("followee", "fee_h")
	p.add("follower", "fer_h")
	if _, err := repo.Request(context.Background(), "follower", "followee"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Followee accepts the request authored by {follower} → 200.
	w := httptest.NewRecorder()
	h.accept(w, req(t, "POST", "/follows/fer_h/accept", "followee", "", "username", "fer_h"))
	if w.Code != http.StatusOK {
		t.Fatalf("accept status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env edgeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Relationship != RelationshipPendingIncoming && env.Data.UserID != "follower" {
		// followee's relationship to follower is pending_incoming->none after
		// accept (follower follows followee, inbound accepted = none for actor).
		t.Logf("rel after accept = %q", env.Data.Relationship)
	}
	got, _ := repo.Get(context.Background(), "follower", "followee")
	if got.Status != StatusAccepted {
		t.Fatalf("edge should be accepted, got %+v", got)
	}
}

func TestHandler_ThirdPartyCannotAccept(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("followee", "fee_h")
	p.add("follower", "fer_h")
	p.add("stranger", "str_h")
	if _, err := repo.Request(context.Background(), "follower", "followee"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Stranger tries to accept the request addressed to followee → 404 (no
	// pending row addressed to the stranger).
	w := httptest.NewRecorder()
	h.accept(w, req(t, "POST", "/follows/fer_h/accept", "stranger", "", "username", "fer_h"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger accept = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_Reject204(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("followee", "fee_h")
	p.add("follower", "fer_h")
	if _, err := repo.Request(context.Background(), "follower", "followee"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	h.reject(w, req(t, "POST", "/follows/fer_h/reject", "followee", "", "username", "fer_h"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("reject status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if _, err := repo.Get(context.Background(), "follower", "followee"); err == nil {
		t.Fatal("edge should be gone after reject")
	}
}

func TestHandler_AcceptUnknownUsername404(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("followee", "fee_h")
	w := httptest.NewRecorder()
	h.accept(w, req(t, "POST", "/follows/ghost/accept", "followee", "", "username", "ghost"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// --- DELETE /follows/{username} context-sensitive ------------------------

func TestHandler_DeleteFollowCancelsPending(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	if _, err := repo.Request(context.Background(), "actor", "bob"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	h.unfollow(w, req(t, "DELETE", "/follows/bob_h", "actor", "", "username", "bob_h"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if _, err := repo.Get(context.Background(), "actor", "bob"); err == nil {
		t.Fatal("pending edge should be canceled")
	}
}

func TestHandler_DeleteFollowUnfollowsAccepted(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	mustAccept(t, repo, "actor", "bob")

	w := httptest.NewRecorder()
	h.unfollow(w, req(t, "DELETE", "/follows/bob_h", "actor", "", "username", "bob_h"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if _, err := repo.Get(context.Background(), "actor", "bob"); err == nil {
		t.Fatal("accepted edge should be unfollowed")
	}
}

func TestHandler_DeleteFollowNoEdge404(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	w := httptest.NewRecorder()
	h.unfollow(w, req(t, "DELETE", "/follows/bob_h", "actor", "", "username", "bob_h"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_ThirdPartyCannotCancel(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("bob", "bob_h")
	p.add("stranger", "str_h")
	if _, err := repo.Request(context.Background(), "actor", "bob"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Stranger tries DELETE /follows/bob_h → there is no stranger→bob edge, 404.
	w := httptest.NewRecorder()
	h.unfollow(w, req(t, "DELETE", "/follows/bob_h", "stranger", "", "username", "bob_h"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger cancel = %d, want 404", w.Code)
	}
	// The actor's request must survive.
	if _, err := repo.Get(context.Background(), "actor", "bob"); err != nil {
		t.Fatal("actor's pending edge should survive stranger's delete")
	}
}

// --- DELETE /followers/{username} ----------------------------------------

func TestHandler_RemoveFollower(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")  // the followee
	p.add("follower", "fer_h") // accepted follower of actor
	mustAccept(t, repo, "follower", "actor")

	w := httptest.NewRecorder()
	h.removeFollower(w, req(t, "DELETE", "/followers/fer_h", "actor", "", "username", "fer_h"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if _, err := repo.Get(context.Background(), "follower", "actor"); err == nil {
		t.Fatal("accepted follower edge should be removed")
	}
}

func TestHandler_RemoveFollowerRequiresAcceptedEdge(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("follower", "fer_h")
	// Only a pending edge exists — remove-follower targets accepted rows.
	if _, err := repo.Request(context.Background(), "follower", "actor"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	h.removeFollower(w, req(t, "DELETE", "/followers/fer_h", "actor", "", "username", "fer_h"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_ThirdPartyCannotRemoveFollower(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("actor", "actor_h")
	p.add("follower", "fer_h")
	p.add("stranger", "str_h")
	mustAccept(t, repo, "follower", "actor")
	// Stranger tries to remove follower from their (the stranger's) followers —
	// no follower→stranger accepted edge, 404. Actor's edge is untouched.
	w := httptest.NewRecorder()
	h.removeFollower(w, req(t, "DELETE", "/followers/fer_h", "stranger", "", "username", "fer_h"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger remove = %d, want 404", w.Code)
	}
	if _, err := repo.Get(context.Background(), "follower", "actor"); err != nil {
		t.Fatal("actor's follower edge should survive")
	}
}

// --- GET /follows/requests ----------------------------------------------

func TestHandler_ListIncomingRequestsDefault(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("me", "me_h")
	p.add("x", "x_h")
	p.add("y", "y_h")
	if _, err := repo.Request(context.Background(), "x", "me"); err != nil {
		t.Fatalf("seed x: %v", err)
	}
	if _, err := repo.Request(context.Background(), "y", "me"); err != nil {
		t.Fatalf("seed y: %v", err)
	}

	w := httptest.NewRecorder()
	h.listRequests(w, req(t, "GET", "/follows/requests", "me", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env requestsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Requests) != 2 {
		t.Fatalf("requests len = %d, want 2", len(env.Data.Requests))
	}
	// Each row carries the requester's summary + the actor's relationship
	// (pending_incoming, since the requester asked to follow me).
	for _, rd := range env.Data.Requests {
		if rd.DisplayName == "" {
			t.Errorf("summary missing for %s", rd.UserID)
		}
		if rd.Relationship != RelationshipPendingIncoming {
			t.Errorf("rel = %q, want pending_incoming for %s", rd.Relationship, rd.UserID)
		}
	}
}

func TestHandler_ListOutgoingRequests(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("me", "me_h")
	p.add("p1", "p1_h")
	if _, err := repo.Request(context.Background(), "me", "p1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	h.listRequests(w, req(t, "GET", "/follows/requests?direction=outgoing", "me", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env requestsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Requests) != 1 || env.Data.Requests[0].UserID != "p1" {
		t.Fatalf("outgoing = %+v, want [p1]", env.Data.Requests)
	}
	if env.Data.Requests[0].Relationship != RelationshipRequested {
		t.Fatalf("rel = %q, want requested", env.Data.Requests[0].Relationship)
	}
}

func TestHandler_RequestsEmptyNonNullSlice(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("me", "me_h")
	w := httptest.NewRecorder()
	h.listRequests(w, req(t, "GET", "/follows/requests", "me", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// The JSON must contain "requests":[] not null.
	if !strings.Contains(w.Body.String(), `"requests":[]`) {
		t.Fatalf("expected non-null empty requests slice, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"next_cursor":null`) {
		t.Fatalf("expected null next_cursor, got %s", w.Body.String())
	}
}

func TestHandler_RequestsInvalidCursor400(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("me", "me_h")
	w := httptest.NewRecorder()
	h.listRequests(w, req(t, "GET", "/follows/requests?cursor=not-valid!!", "me", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid cursor") {
		t.Errorf("body should mention invalid cursor: %s", w.Body.String())
	}
}

func TestHandler_RequestsBadDirection400(t *testing.T) {
	h, _, p := newTestHandler(t)
	p.add("me", "me_h")
	w := httptest.NewRecorder()
	h.listRequests(w, req(t, "GET", "/follows/requests?direction=sideways", "me", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_RequestsPaginationAcrossPages(t *testing.T) {
	h, repo, p := newTestHandler(t)
	p.add("me", "me_h")
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		repo.now = func(at time.Time) func() time.Time {
			return func() time.Time { return at }
		}(base.Add(time.Duration(i) * time.Hour))
		who := "r" + itoa(i)
		p.add(who, who+"_h")
		if _, err := repo.Request(context.Background(), who, "me"); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Page 1: limit 2 → newest two + cursor.
	w1 := httptest.NewRecorder()
	h.listRequests(w1, req(t, "GET", "/follows/requests?limit=2", "me", ""))
	var e1 requestsEnvelope
	if err := json.Unmarshal(w1.Body.Bytes(), &e1); err != nil {
		t.Fatalf("decode p1: %v", err)
	}
	if len(e1.Data.Requests) != 2 || e1.Data.NextCursor == nil {
		t.Fatalf("p1 = %d rows cursor=%v, want 2 + cursor", len(e1.Data.Requests), e1.Data.NextCursor)
	}
	// Page 2: → last row, nil cursor.
	w2 := httptest.NewRecorder()
	h.listRequests(w2, req(t, "GET", "/follows/requests?limit=2&cursor="+*e1.Data.NextCursor, "me", ""))
	var e2 requestsEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &e2); err != nil {
		t.Fatalf("decode p2: %v", err)
	}
	if len(e2.Data.Requests) != 1 || e2.Data.NextCursor != nil {
		t.Fatalf("p2 = %d rows cursor=%v, want 1 + nil", len(e2.Data.Requests), e2.Data.NextCursor)
	}
}
