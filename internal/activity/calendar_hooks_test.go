package activity

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// recordingSyncer records which activity ids were synced or deleted, so the
// hook wiring is verified rather than assumed.
type recordingSyncer struct {
	synced  []string
	deleted []string
	err     error
}

func (r *recordingSyncer) SyncActivity(ctx context.Context, userID, activityID string) error {
	r.synced = append(r.synced, activityID)
	return r.err
}

func (r *recordingSyncer) DeleteActivityEvent(ctx context.Context, userID, activityID string) error {
	r.deleted = append(r.deleted, activityID)
	return r.err
}

// newSyncerServer is newUnifiedServer plus a recording calendar syncer.
func newSyncerServer(t *testing.T) (http.Handler, *recordingSyncer) {
	t.Helper()
	d := dbtest.New(t)
	repo := NewSQLiteRepository(d, NewMemoryArchiver())
	h := NewHandler(repo)
	h.SetRegistry(NewRegistry(
		NewEnduranceDescriptor(ActivityRunning, NewSQLiteEnduranceDetailStore(d, ActivityRunning)),
	))
	syncer := &recordingSyncer{}
	h.SetCalendarSyncer(syncer)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), testUserID)))
		})
	})
	h.Mount(r)
	return r, syncer
}

const runCreateBody = `{"activity_type":"running","start_time":"2026-08-08T12:00:00Z","duration_seconds":1800,"details":{"distance_meters":5000}}`

func TestCreateUnified_TriggersCalendarSync(t *testing.T) {
	srv, syncer := newSyncerServer(t)

	w := doJSON(t, srv, http.MethodPost, "/activities", runCreateBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /activities = %d, body=%s", w.Code, w.Body.String())
	}
	created := decodeActivity(t, w)

	if len(syncer.synced) != 1 || syncer.synced[0] != created.ID {
		t.Fatalf("synced = %v, want exactly the created activity %s", syncer.synced, created.ID)
	}
}

func TestUpdateUnified_TriggersCalendarSync(t *testing.T) {
	srv, syncer := newSyncerServer(t)
	created := decodeActivity(t, doJSON(t, srv, http.MethodPost, "/activities", runCreateBody))
	syncer.synced = nil

	w := doJSON(t, srv, http.MethodPut, "/activities/"+created.ID,
		`{"activity_type":"running","start_time":"2026-08-08T12:00:00Z","duration_seconds":1900,"name":"Renamed","details":{"distance_meters":5200}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /activities = %d, body=%s", w.Code, w.Body.String())
	}
	if len(syncer.synced) != 1 || syncer.synced[0] != created.ID {
		t.Fatalf("synced = %v, want the updated activity re-synced", syncer.synced)
	}
}

func TestDeleteActivity_TriggersCalendarEventDeletion(t *testing.T) {
	srv, syncer := newSyncerServer(t)
	created := decodeActivity(t, doJSON(t, srv, http.MethodPost, "/activities", runCreateBody))

	w := doJSON(t, srv, http.MethodDelete, "/activities/"+created.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /activities = %d, body=%s", w.Code, w.Body.String())
	}
	if len(syncer.deleted) != 1 || syncer.deleted[0] != created.ID {
		t.Fatalf("deleted = %v, want the deleted activity's event removed", syncer.deleted)
	}
}

// A rename changes the event title, so the synced body would otherwise go
// stale.
func TestPatchActivity_TriggersCalendarSync(t *testing.T) {
	srv, syncer := newSyncerServer(t)
	created := decodeActivity(t, doJSON(t, srv, http.MethodPost, "/activities", runCreateBody))
	syncer.synced = nil

	w := doJSON(t, srv, http.MethodPatch, "/activities/"+created.ID, `{"name":"Morning Run"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /activities = %d, body=%s", w.Code, w.Body.String())
	}
	if len(syncer.synced) != 1 {
		t.Fatalf("synced = %v, want one re-sync after the rename", syncer.synced)
	}
}

// The hooks are best-effort: a Google outage must never fail a user's write.
func TestCalendarSyncFailure_DoesNotFailTheRequest(t *testing.T) {
	srv, syncer := newSyncerServer(t)
	syncer.err = context.DeadlineExceeded

	w := doJSON(t, srv, http.MethodPost, "/activities", runCreateBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /activities = %d, want 201 despite the sync failure; body=%s", w.Code, w.Body.String())
	}
}

// A handler with no syncer wired (every existing test, and any deployment
// without calendar sync configured) must behave exactly as before.
func TestNilCalendarSyncer_IsANoOp(t *testing.T) {
	srv, _ := newUnifiedServer(t)

	w := doJSON(t, srv, http.MethodPost, "/activities", runCreateBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /activities = %d with no syncer wired; body=%s", w.Code, w.Body.String())
	}
}
