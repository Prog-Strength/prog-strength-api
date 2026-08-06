package activity

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/timeline"
)

// recordingPublisher captures every PostRef the handler publishes.
type recordingPublisher struct {
	refs []timeline.PostRef
}

func (p *recordingPublisher) EnsurePost(_ context.Context, ref timeline.PostRef) error {
	p.refs = append(p.refs, ref)
	return nil
}

// recordingMatcher captures planned-workout match calls so the tests can assert
// that widening feed coverage did NOT widen plan matching.
type recordingMatcher struct {
	logged []string
}

func (m *recordingMatcher) OnSessionLogged(_ context.Context, _ string, ref SessionRef) {
	m.logged = append(m.logged, ref.SessionID)
}

func (m *recordingMatcher) OnSessionDeleted(_ context.Context, _, _ string) {}

// newPublishingServer is newUnifiedServer plus hiking (the type the feed used
// to drop) and the two best-effort side-effect seams wired to recorders.
func newPublishingServer(t *testing.T) (http.Handler, *recordingPublisher, *recordingMatcher) {
	t.Helper()
	d := dbtest.New(t)
	repo := NewSQLiteRepository(d, NewMemoryArchiver())
	h := NewHandler(repo)
	h.SetRegistry(NewRegistry(
		NewEnduranceDescriptor(ActivityRunning, NewSQLiteEnduranceDetailStore(d, ActivityRunning)),
		NewEnduranceDescriptor(ActivityHiking, NewSQLiteEnduranceDetailStore(d, ActivityHiking)),
		NewEnduranceDescriptor(ActivityWalking, NewSQLiteEnduranceDetailStore(d, ActivityWalking)),
	))
	pub := &recordingPublisher{}
	matcher := &recordingMatcher{}
	h.SetPublisher(pub)
	h.SetPlanMatcher(matcher)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), testUserID)))
		})
	})
	h.Mount(r)
	return r, pub, matcher
}

// TestCreateActivity_PublishesEveryType is the regression guard for the issue:
// logging a hike must produce a feed post. Before this change the publish hook
// was gated on `desc.Type == ActivityRunning`, so a hike wrote its row and
// silently never reached the social feed. Every registered type publishes now,
// all under the coarse `activity` source type — the property that has to hold
// for the next type anyone registers.
func TestCreateActivity_PublishesEveryType(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "hiking",
			body: `{"activity_type":"hiking","start_time":"2026-07-04T08:00:00Z","name":"Franconia Ridge","duration_seconds":21600,"details":{"distance_meters":14484.1}}`,
		},
		{
			name: "running",
			body: `{"activity_type":"running","start_time":"2026-07-05T07:00:00Z","name":"Morning run","duration_seconds":2472,"details":{"distance_meters":8046.72}}`,
		},
		{
			name: "walking",
			body: `{"activity_type":"walking","start_time":"2026-07-06T07:00:00Z","name":"Evening walk","duration_seconds":1800,"details":{"distance_meters":3000}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pub, _ := newPublishingServer(t)
			w := doJSON(t, srv, http.MethodPost, "/activities", tc.body)
			if w.Code != http.StatusCreated {
				t.Fatalf("POST /activities = %d, want 201; body=%s", w.Code, w.Body.String())
			}
			created := decodeActivity(t, w)

			if len(pub.refs) != 1 {
				t.Fatalf("published %d refs, want exactly 1: %+v", len(pub.refs), pub.refs)
			}
			ref := pub.refs[0]
			if ref.SourceType != timeline.SourceActivity {
				t.Errorf("source_type = %q, want activity", ref.SourceType)
			}
			if ref.SourceID != created.ID {
				t.Errorf("source_id = %q, want the created activity %q", ref.SourceID, created.ID)
			}
			if ref.UserID != testUserID {
				t.Errorf("user_id = %q, want %q", ref.UserID, testUserID)
			}
			if ref.OccurredAt.IsZero() {
				t.Error("occurred_at is zero — the post would sort to the bottom of the feed forever")
			}
		})
	}
}

// TestCreateActivity_PlanMatchingStaysRunningOnly pins the deliberate asymmetry:
// feed publishing is now type-agnostic, but planned-workout reconciliation is
// not — the planner's endurance side models runs, and widening it is a product
// decision, not a side effect of fixing feed coverage.
func TestCreateActivity_PlanMatchingStaysRunningOnly(t *testing.T) {
	srv, _, matcher := newPublishingServer(t)

	hike := `{"activity_type":"hiking","start_time":"2026-07-04T08:00:00Z","name":"Franconia Ridge","duration_seconds":21600,"details":{"distance_meters":14484.1}}`
	if w := doJSON(t, srv, http.MethodPost, "/activities", hike); w.Code != http.StatusCreated {
		t.Fatalf("POST hike = %d; body=%s", w.Code, w.Body.String())
	}
	if len(matcher.logged) != 0 {
		t.Errorf("hike triggered plan matching (%v); it should stay running-only", matcher.logged)
	}

	run := `{"activity_type":"running","start_time":"2026-07-05T07:00:00Z","name":"Morning run","duration_seconds":2472,"details":{"distance_meters":8046.72}}`
	if w := doJSON(t, srv, http.MethodPost, "/activities", run); w.Code != http.StatusCreated {
		t.Fatalf("POST run = %d; body=%s", w.Code, w.Body.String())
	}
	if len(matcher.logged) != 1 {
		t.Errorf("run plan-match calls = %d, want 1", len(matcher.logged))
	}
}
