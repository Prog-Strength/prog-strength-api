package activity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestEnduranceDescriptor_SummarizeMirrorsRunCard(t *testing.T) {
	d := NewEnduranceDescriptor(ActivityRunning, nil)

	a := Activity{
		Name:            ptrStr("Morning Run"),
		DistanceMeters:  5 * 1609.344, // 5.0 mi
		DurationSeconds: 2472,         // 41:12
	}
	got := d.Summarize(a, nil)
	want := Summary{
		Title:    "Morning Run",
		Subtitle: "5.0 mi · 41:12",
		Metrics:  []string{"5.0 mi", "41:12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Summarize = %+v, want %+v", got, want)
	}
}

func TestEnduranceDescriptor_SummarizeDefaultTitlePerSport(t *testing.T) {
	cases := []struct {
		typ  ActivityType
		want string
	}{
		{ActivityRunning, "Run"},
		{ActivityWalking, "Walk"},
		{ActivityCycling, "Ride"},
		{ActivityOther, "Activity"},
	}
	for _, tc := range cases {
		d := NewEnduranceDescriptor(tc.typ, nil)
		// Nameless and blank-named activities both fall back to the label.
		for _, name := range []*string{nil, ptrStr("  ")} {
			got := d.Summarize(Activity{Name: name}, nil)
			if got.Title != tc.want {
				t.Fatalf("Summarize(%s, name=%v).Title = %q, want %q", tc.typ, name, got.Title, tc.want)
			}
		}
	}
}

func TestEnduranceDescriptor_SummarizePrefersLoadedDetails(t *testing.T) {
	d := NewEnduranceDescriptor(ActivityRunning, nil)
	// The base row carries no endurance fields (e.g. a caller that skipped
	// the join); the loaded details win.
	got := d.Summarize(Activity{DurationSeconds: 600}, &EnduranceDetails{DistanceMeters: 1609.344})
	if got.Metrics[0] != "1.0 mi" {
		t.Fatalf("Summarize with details = %+v, want distance from details (1.0 mi)", got)
	}
}

func TestEnduranceDescriptor_ValidateCreate(t *testing.T) {
	details := func(s string) json.RawMessage { return json.RawMessage(s) }

	cases := []struct {
		name    string
		typ     ActivityType
		details json.RawMessage
		wantErr bool
	}{
		{"running with distance ok", ActivityRunning, details(`{"distance_meters": 5000}`), false},
		{"running missing details", ActivityRunning, nil, true},
		{"running null details", ActivityRunning, details(`null`), true},
		{"running zero distance", ActivityRunning, details(`{"distance_meters": 0}`), true},
		{"walking negative distance", ActivityWalking, details(`{"distance_meters": -1}`), true},
		{"cycling missing distance", ActivityCycling, details(`{}`), true},
		{"other without details ok", ActivityOther, nil, false},
		{"other with distance ok", ActivityOther, details(`{"distance_meters": 100}`), false},
		{"other negative distance", ActivityOther, details(`{"distance_meters": -5}`), true},
		{"malformed json", ActivityRunning, details(`{`), true},
		{"unknown field rejected", ActivityRunning, details(`{"distance_meters": 5000, "distancee": 1}`), true},
		{"bad environment", ActivityRunning, details(`{"distance_meters": 5000, "environment": "underwater"}`), true},
		{"indoor environment ok", ActivityRunning, details(`{"distance_meters": 5000, "environment": "indoor"}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewEnduranceDescriptor(tc.typ, nil)
			err := d.ValidateCreate(CreateRequest{Type: tc.typ, StartTime: time.Now(), Details: tc.details})
			if tc.wantErr && err == nil {
				t.Fatal("ValidateCreate = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateCreate = %v, want nil", err)
			}
		})
	}
}

// seedBaseActivity inserts a bare activities base row so detail-store tests
// have something to hang details off without going through the TCX Create
// path.
func seedBaseActivity(t *testing.T, conn *sql.DB, id, userID string, typ ActivityType) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := conn.ExecContext(context.Background(), `
		INSERT INTO activities (id, user_id, activity_type, start_time, ingest_source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'manual', ?, ?)
	`, id, userID, typ, now, now, now); err != nil {
		t.Fatalf("seed activity %s: %v", id, err)
	}
}

func TestSQLiteEnduranceDetailStore_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := newMigratedDB(t)
	store := NewSQLiteEnduranceDetailStore(conn, ActivityRunning)
	seedBaseActivity(t, conn, "a1", "u1", ActivityRunning)

	details := &EnduranceDetails{
		DistanceMeters:      5000,
		RawDistanceMeters:   5000,
		AvgPaceSecPerKm:     ptrF(300),
		BestPaceSecPerKm:    ptrF(280),
		ElevationGainMeters: ptrF(42),
		Environment:         EnvironmentOutdoor,
		RouteGeoJSON:        ptrStr(`{"type":"Feature"}`),
	}
	if err := store.Save(ctx, "u1", "a1", details); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(ctx, "u1", "a1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, details) {
		t.Fatalf("Load = %+v, want %+v", got, details)
	}

	// Save again with a changed distance: upsert, not duplicate-key error.
	details.DistanceMeters = 5200
	if saveErr := store.Save(ctx, "u1", "a1", details); saveErr != nil {
		t.Fatalf("Save (upsert): %v", saveErr)
	}
	got, err = store.Load(ctx, "u1", "a1")
	if err != nil {
		t.Fatalf("Load after upsert: %v", err)
	}
	if got.(*EnduranceDetails).DistanceMeters != 5200 {
		t.Fatalf("Load after upsert distance = %v, want 5200", got.(*EnduranceDetails).DistanceMeters)
	}

	if err := store.Delete(ctx, "u1", "a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load(ctx, "u1", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after delete err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteEnduranceDetailStore_EnforcesOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := newMigratedDB(t)
	store := NewSQLiteEnduranceDetailStore(conn, ActivityRunning)
	seedBaseActivity(t, conn, "a1", "u1", ActivityRunning)

	details := &EnduranceDetails{DistanceMeters: 5000, RawDistanceMeters: 5000, Environment: EnvironmentOutdoor}
	if err := store.Save(ctx, "intruder", "a1", details); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save as wrong user err = %v, want ErrNotFound", err)
	}
	if err := store.Save(ctx, "u1", "a1", details); err != nil {
		t.Fatalf("Save as owner: %v", err)
	}
	if _, err := store.Load(ctx, "intruder", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load as wrong user err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "intruder", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete as wrong user err = %v, want ErrNotFound", err)
	}
	// The intruder's delete attempt must not have removed the row.
	if _, err := store.Load(ctx, "u1", "a1"); err != nil {
		t.Fatalf("Load as owner after intruder delete: %v", err)
	}
}

func TestNewSQLiteEnduranceDetailStore_PanicsForBaseOnlyType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for a type without a detail table")
		}
	}()
	NewSQLiteEnduranceDetailStore(nil, ActivityStrengthTraining)
}
