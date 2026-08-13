package calendarsync

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// mustExec runs a statement against db and fails the test on error, so seeding
// stays readable in the test bodies below.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

var linkNow = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

func TestLinksForUser(t *testing.T) {
	database := dbtest.New(t)
	ctx := context.Background()

	mustExec(t, database, `INSERT INTO planned_workouts
		(id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc, timezone, status, google_event_id, created_at, updated_at)
		VALUES ('pw_1','u1','Upper Push','lift',?,?,'UTC','planned','g_pw1',?,?)`,
		linkNow, linkNow.Add(time.Hour), linkNow, linkNow)
	// Soft-deleted plans must not mark anything.
	mustExec(t, database, `INSERT INTO planned_workouts
		(id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc, timezone, status, google_event_id, created_at, updated_at, deleted_at)
		VALUES ('pw_2','u1','Deleted','lift',?,?,'UTC','planned','g_pw2',?,?,?)`,
		linkNow, linkNow.Add(time.Hour), linkNow, linkNow, linkNow)
	// Another user's plan must not leak.
	mustExec(t, database, `INSERT INTO planned_workouts
		(id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc, timezone, status, google_event_id, created_at, updated_at)
		VALUES ('pw_3','u2','Theirs','lift',?,?,'UTC','planned','g_pw3',?,?)`,
		linkNow, linkNow.Add(time.Hour), linkNow, linkNow)

	// activity_calendar_sync.activity_id is a foreign key onto activities, and
	// the test database runs with foreign keys on, so the sessions have to
	// exist before their sync rows do.
	seedActivity(t, database, "act_1", "u1", linkNow)
	seedActivity(t, database, "act_2", "u1", linkNow)
	mustExec(t, database, `INSERT INTO activity_calendar_sync
		(activity_id, user_id, google_event_id, sync_status, attempts, updated_at)
		VALUES ('act_1','u1','g_act1','synced',0,?)`, linkNow)
	// A row that never got an event id is not a link.
	mustExec(t, database, `INSERT INTO activity_calendar_sync
		(activity_id, user_id, google_event_id, sync_status, attempts, updated_at)
		VALUES ('act_2','u1',NULL,'pending',0,?)`, linkNow)

	repo := NewSQLiteEventLinkRepository(database)
	links, err := repo.LinksForUser(ctx, "u1", linkNow.Add(-24*time.Hour), linkNow.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("LinksForUser: %v", err)
	}

	want := map[string]EventLink{
		"g_pw1":  {Kind: LinkKindPlannedWorkout, ID: "pw_1"},
		"g_act1": {Kind: LinkKindActivity, ID: "act_1"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("links = %#v, want %#v", links, want)
	}
}

func TestLinksForUser_WindowExcludesFarPlans(t *testing.T) {
	database := dbtest.New(t)
	mustExec(t, database, `INSERT INTO planned_workouts
		(id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc, timezone, status, google_event_id, created_at, updated_at)
		VALUES ('pw_far','u1','Next month','lift',?,?,'UTC','planned','g_far',?,?)`,
		linkNow.AddDate(0, 1, 0), linkNow.AddDate(0, 1, 0).Add(time.Hour), linkNow, linkNow)

	links, err := NewSQLiteEventLinkRepository(database).
		LinksForUser(context.Background(), "u1", linkNow, linkNow.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("LinksForUser: %v", err)
	}
	if _, ok := links["g_far"]; ok {
		t.Error("a plan outside the window must not be in the link map")
	}
}

// A plan that handed its event to the activity that completed it leaves both
// rows pointing at the same Google id. The activity is what actually happened,
// so it must win the link.
func TestLinksForUser_ActivityWinsAHandedOverEvent(t *testing.T) {
	database := dbtest.New(t)
	mustExec(t, database, `INSERT INTO planned_workouts
		(id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc, timezone, status, google_event_id, created_at, updated_at)
		VALUES ('pw_1','u1','Upper Push','lift',?,?,'UTC','completed','g_shared',?,?)`,
		linkNow, linkNow.Add(time.Hour), linkNow, linkNow)
	seedActivity(t, database, "act_1", "u1", linkNow)
	mustExec(t, database, `INSERT INTO activity_calendar_sync
		(activity_id, user_id, google_event_id, sync_status, attempts, updated_at)
		VALUES ('act_1','u1','g_shared','synced',0,?)`, linkNow)

	links, err := NewSQLiteEventLinkRepository(database).
		LinksForUser(context.Background(), "u1", linkNow.Add(-24*time.Hour), linkNow.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("LinksForUser: %v", err)
	}
	want := EventLink{Kind: LinkKindActivity, ID: "act_1"}
	if links["g_shared"] != want {
		t.Errorf("links[g_shared] = %#v, want %#v", links["g_shared"], want)
	}
}

// Another user's synced activity must never appear in this user's map: the
// activity side is bounded by user alone, so the user predicate is the only
// thing keeping it separate.
func TestLinksForUser_ActivitiesAreUserScoped(t *testing.T) {
	database := dbtest.New(t)
	seedActivity(t, database, "act_theirs", "u2", linkNow)
	mustExec(t, database, `INSERT INTO activity_calendar_sync
		(activity_id, user_id, google_event_id, sync_status, attempts, updated_at)
		VALUES ('act_theirs','u2','g_theirs','synced',0,?)`, linkNow)

	links, err := NewSQLiteEventLinkRepository(database).
		LinksForUser(context.Background(), "u1", linkNow.Add(-24*time.Hour), linkNow.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("LinksForUser: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links = %#v, want empty for a user with nothing of their own", links)
	}
}
