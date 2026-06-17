package plannedworkout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// planEnvelope mirrors the httpresp success shape with the plan DTO typed.
type planEnvelope struct {
	Message string  `json:"message"`
	Data    planDTO `json:"data"`
}

// planListEnvelope mirrors the httpresp success shape with a slice DTO typed.
type planListEnvelope struct {
	Message string    `json:"message"`
	Data    []planDTO `json:"data"`
}

// do routes a request through a chi router that has the handler mounted, so
// the {id} URL param is populated exactly as it is in production. The request
// runs as userID-in-context. userRepo may be nil for paths that don't use it.
func do(t *testing.T, repo Repository, userRepo user.Repository, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo, userRepo)
	r := chi.NewRouter()
	h.Mount(r)

	var reqBody *strings.Reader
	if body == "" {
		reqBody = strings.NewReader("")
	} else {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodePlan(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) planDTO {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status: got %d want %d, body=%s", w.Code, wantStatus, w.Body.String())
	}
	var got planEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) []planDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got planListEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}

func seedUser(t *testing.T, repo user.Repository, tz string) *user.User {
	t.Helper()
	u := &user.User{
		Email:        "u@example.com",
		DisplayName:  "U",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
		Timezone:     tz,
	}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

func TestCreateHandler_BareBlock(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"America/New_York"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	created := decodePlan(t, w, http.StatusCreated)
	if created.Status != "planned" {
		t.Errorf("status = %q, want planned", created.Status)
	}
	if created.Timezone != "America/New_York" {
		t.Errorf("timezone = %q, want America/New_York", created.Timezone)
	}
	if created.ActivityKind != "lift" {
		t.Errorf("activity_kind = %q, want lift", created.ActivityKind)
	}

	// GET /{id} returns it with status "planned".
	g := do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, "")
	got := decodePlan(t, g, http.StatusOK)
	if got.ID != created.ID || got.Status != "planned" {
		t.Errorf("get: got %+v", got)
	}
}

func TestCreateHandler_WithAgenda(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"scheduled_start":"2026-07-01T09:00:00Z",
		"scheduled_end":"2026-07-01T10:00:00Z",
		"timezone":"UTC",
		"exercises":[
			{"exercise_id":"squat","sets":[{"target_reps":5,"target_weight":225,"unit":"lb"},{"target_reps":5,"target_weight":225,"unit":"lb"}]},
			{"exercise_id":"bench","sets":[{"target_reps":8,"target_rpe":7}]}
		]
	}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	created := decodePlan(t, w, http.StatusCreated)

	g := do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, "")
	got := decodePlan(t, g, http.StatusOK)
	if len(got.Exercises) != 2 {
		t.Fatalf("exercises = %d, want 2", len(got.Exercises))
	}
	if got.Exercises[0].ExerciseID != "squat" || got.Exercises[0].OrderIndex != 0 {
		t.Errorf("exercise[0] = %+v", got.Exercises[0])
	}
	if got.Exercises[1].ExerciseID != "bench" || got.Exercises[1].OrderIndex != 1 {
		t.Errorf("exercise[1] = %+v", got.Exercises[1])
	}
	if len(got.Exercises[0].Sets) != 2 {
		t.Fatalf("squat sets = %d, want 2", len(got.Exercises[0].Sets))
	}
	if got.Exercises[0].Sets[0].TargetReps == nil || *got.Exercises[0].Sets[0].TargetReps != 5 {
		t.Errorf("squat set[0] target_reps = %v, want 5", got.Exercises[0].Sets[0].TargetReps)
	}
	if got.Exercises[0].Sets[0].OrderIndex != 0 || got.Exercises[0].Sets[1].OrderIndex != 1 {
		t.Errorf("set order: %+v", got.Exercises[0].Sets)
	}
	if got.Exercises[1].Sets[0].TargetRPE == nil || *got.Exercises[1].Sets[0].TargetRPE != 7 {
		t.Errorf("bench set rpe = %v, want 7", got.Exercises[1].Sets[0].TargetRPE)
	}
}

func TestCreateHandler_Run(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"activity_kind":"run",
		"scheduled_start":"2026-07-01T06:00:00Z",
		"scheduled_end":"2026-07-01T07:00:00Z",
		"timezone":"UTC",
		"run_type":"intervals",
		"run_details":"4x800m @ 5k pace, 90s jog recovery"
	}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	created := decodePlan(t, w, http.StatusCreated)
	if created.ActivityKind != "run" {
		t.Errorf("activity_kind = %q, want run", created.ActivityKind)
	}

	g := do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, "")
	got := decodePlan(t, g, http.StatusOK)
	if got.RunType == nil || *got.RunType != "intervals" {
		t.Errorf("run_type = %v, want intervals", got.RunType)
	}
	if got.RunDetails == nil || *got.RunDetails != "4x800m @ 5k pace, 90s jog recovery" {
		t.Errorf("run_details = %v", got.RunDetails)
	}
	if len(got.Exercises) != 0 {
		t.Errorf("run carried exercises: %+v", got.Exercises)
	}
}

func TestCreateHandler_RunWithExercisesIs400(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"activity_kind":"run",
		"scheduled_start":"2026-07-01T06:00:00Z",
		"scheduled_end":"2026-07-01T07:00:00Z",
		"timezone":"UTC",
		"exercises":[{"exercise_id":"squat","sets":[]}]
	}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (run can't carry exercises)", w.Code)
	}
}

func TestUpdateHandler_SwitchLiftToRunClearsAgenda(t *testing.T) {
	repo := NewMemoryRepository()
	// Start as a lift with an agenda.
	create := `{
		"scheduled_start":"2026-07-01T09:00:00Z",
		"scheduled_end":"2026-07-01T10:00:00Z",
		"timezone":"UTC",
		"exercises":[{"exercise_id":"squat","sets":[{"target_reps":5}]}]
	}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", create), http.StatusCreated)

	// Switch it to a run; the server drops the lift agenda so the result is coherent.
	upd := `{"activity_kind":"run","run_type":"easy","run_details":"conversational 5 miles"}`
	updated := decodePlan(t, do(t, repo, nil, "u1", "PUT", "/planned-workouts/"+created.ID, upd), http.StatusOK)
	if updated.ActivityKind != "run" {
		t.Errorf("activity_kind = %q, want run", updated.ActivityKind)
	}
	if len(updated.Exercises) != 0 {
		t.Errorf("expected lift agenda cleared on switch, got: %+v", updated.Exercises)
	}
	if updated.RunType == nil || *updated.RunType != "easy" {
		t.Errorf("run_type = %v, want easy", updated.RunType)
	}
}

func TestCreateHandler_DefaultsTimezoneFromUser(t *testing.T) {
	repo := NewMemoryRepository()
	userRepo := user.NewMemoryRepository()
	u := seedUser(t, userRepo, "America/New_York")

	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z"}`
	w := do(t, repo, userRepo, u.ID, "POST", "/planned-workouts/", body)
	created := decodePlan(t, w, http.StatusCreated)
	if created.Timezone != "America/New_York" {
		t.Errorf("timezone = %q, want America/New_York (from user)", created.Timezone)
	}
}

func TestCreateHandler_DefaultsTimezoneUTCOnRepoError(t *testing.T) {
	repo := NewMemoryRepository()
	userRepo := user.NewMemoryRepository()
	// No user seeded → GetByID returns ErrNotFound → fallback "UTC".
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z"}`
	w := do(t, repo, userRepo, "missing-user", "POST", "/planned-workouts/", body)
	created := decodePlan(t, w, http.StatusCreated)
	if created.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC (fallback)", created.Timezone)
	}
}

func TestListHandler_FiltersByRange(t *testing.T) {
	repo := NewMemoryRepository()
	mk := func(start, end string) string {
		return `{"scheduled_start":"` + start + `","scheduled_end":"` + end + `","timezone":"UTC"}`
	}
	// In range.
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mk("2026-07-05T09:00:00Z", "2026-07-05T10:00:00Z"))
	// Out of range (before).
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mk("2026-06-01T09:00:00Z", "2026-06-01T10:00:00Z"))
	// Out of range (after).
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mk("2026-08-01T09:00:00Z", "2026-08-01T10:00:00Z"))

	w := do(t, repo, nil, "u1", "GET", "/planned-workouts/?since=2026-07-01T00:00:00Z&until=2026-08-01T00:00:00Z", "")
	got := decodeList(t, w)
	if len(got) != 1 {
		t.Fatalf("got %d plans, want 1: %+v", len(got), got)
	}
}

func TestListHandler_BadSince(t *testing.T) {
	repo := NewMemoryRepository()
	w := do(t, repo, nil, "u1", "GET", "/planned-workouts/?since=nope", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

// TestListHandler_TimezoneDateWindow is the regression lock for the planned-
// workout lookup bug. Two plans sit on the same Denver-local day (2026-06-17):
// one at noon, one at 7 PM. The 7 PM plan is 2026-06-18T01:00:00Z in UTC — past
// midnight UTC. The old behavior had the model query the UTC day
// [00:00Z, 24:00Z), which dropped the evening plan; the timezone-aware contract
// queries the Denver day [06:00Z, +1d 06:00Z) and returns both.
func TestListHandler_TimezoneDateWindow(t *testing.T) {
	repo := NewMemoryRepository()
	mk := func(start, end string) string {
		return `{"scheduled_start":"` + start + `","scheduled_end":"` + end + `","timezone":"America/Denver"}`
	}
	// Noon Denver (UTC-6) = 18:00Z.
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mk("2026-06-17T18:00:00Z", "2026-06-17T19:00:00Z"))
	// 7 PM Denver = next-day 01:00Z — the session the UTC-day window dropped.
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mk("2026-06-18T01:00:00Z", "2026-06-18T02:00:00Z"))

	// Timezone-aware contract: both plans land in the Denver day.
	w := do(t, repo, nil, "u1", "GET", "/planned-workouts/?timezone=America/Denver&date=2026-06-17", "")
	if got := decodeList(t, w); len(got) != 2 {
		t.Fatalf("timezone window: got %d plans, want 2: %+v", len(got), got)
	}

	// The old UTC-day window the model used to build drops the evening plan —
	// documents the bug the contract fixes.
	w = do(t, repo, nil, "u1", "GET", "/planned-workouts/?since=2026-06-17T00:00:00Z&until=2026-06-18T00:00:00Z", "")
	if got := decodeList(t, w); len(got) != 1 {
		t.Fatalf("UTC-day window: got %d plans, want 1: %+v", len(got), got)
	}
}

// TestDTO_RendersScheduledTimesInPlanTimezone locks the display fix: the DTO
// renders scheduled_start/end in the plan's own timezone (offset-aware), so the
// wall-clock the chat model reads is the local time. 2026-06-18T00:00:00Z is
// 6 PM MDT on 2026-06-17 — the plan the model previously misread as "midnight
// tomorrow" and dropped from "today" because the DTO emitted UTC.
func TestDTO_RendersScheduledTimesInPlanTimezone(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-06-18T00:00:00Z","scheduled_end":"2026-06-18T01:00:00Z","timezone":"America/Denver"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			ScheduledStart string `json:"scheduled_start"`
			ScheduledEnd   string `json:"scheduled_end"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ScheduledStart != "2026-06-17T18:00:00-06:00" {
		t.Errorf("scheduled_start = %q, want 2026-06-17T18:00:00-06:00 (local, offset-aware)", env.Data.ScheduledStart)
	}
	if env.Data.ScheduledEnd != "2026-06-17T19:00:00-06:00" {
		t.Errorf("scheduled_end = %q, want 2026-06-17T19:00:00-06:00", env.Data.ScheduledEnd)
	}
}

// TestListHandler_RendersAllPlansInRequestTimezone is the regression lock for
// the "second workout shows as tomorrow" bug. Two plans land on the same Denver
// day but carry DIFFERENT stored timezones: one America/Denver, one UTC (e.g.
// created via different paths). Listed with timezone=America/Denver, BOTH must
// render on the local date 2026-06-17 — the UTC-stored plan must not surface as
// "June 18" just because its creation timezone differs from the viewer's.
func TestListHandler_RendersAllPlansInRequestTimezone(t *testing.T) {
	repo := NewMemoryRepository()
	mkTZ := func(start, end, tz string) string {
		return `{"scheduled_start":"` + start + `","scheduled_end":"` + end + `","timezone":"` + tz + `"}`
	}
	// Noon Denver, stored in Denver.
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mkTZ("2026-06-17T18:00:00Z", "2026-06-17T19:00:00Z", "America/Denver"))
	// 6 PM Denver (00:00Z next day), but stored with a UTC timezone — the case
	// that rendered as "midnight June 18" before this fix.
	do(t, repo, nil, "u1", "POST", "/planned-workouts/", mkTZ("2026-06-18T00:00:00Z", "2026-06-18T01:00:00Z", "UTC"))

	w := do(t, repo, nil, "u1", "GET", "/planned-workouts/?timezone=America/Denver&date=2026-06-17", "")
	var env struct {
		Data []struct {
			ScheduledStart string `json:"scheduled_start"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("got %d plans, want 2", len(env.Data))
	}
	for _, p := range env.Data {
		// Every returned plan renders on the Denver local date with the Denver
		// offset, regardless of its stored timezone.
		if !strings.HasPrefix(p.ScheduledStart, "2026-06-17T") || !strings.HasSuffix(p.ScheduledStart, "-06:00") {
			t.Errorf("scheduled_start = %q, want 2026-06-17T…-06:00 (rendered in request tz)", p.ScheduledStart)
		}
	}
}

func TestListHandler_BadTimezone(t *testing.T) {
	repo := NewMemoryRepository()
	w := do(t, repo, nil, "u1", "GET", "/planned-workouts/?timezone=Not/AZone&date=2026-06-17", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateHandler_RescheduleAndReplaceAgenda(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC",
		"exercises":[{"exercise_id":"squat","sets":[{"target_reps":5}]}]
	}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", body), http.StatusCreated)

	upd := `{
		"scheduled_start":"2026-07-02T11:00:00Z","scheduled_end":"2026-07-02T12:30:00Z",
		"exercises":[{"exercise_id":"deadlift","sets":[{"target_reps":3}]},{"exercise_id":"row","sets":[]}]
	}`
	w := do(t, repo, nil, "u1", "PUT", "/planned-workouts/"+created.ID, upd)
	got := decodePlan(t, w, http.StatusOK)
	if !got.ScheduledStart.Equal(mustTime(t, "2026-07-02T11:00:00Z")) {
		t.Errorf("scheduled_start = %v, want 2026-07-02T11:00:00Z", got.ScheduledStart)
	}
	if len(got.Exercises) != 2 || got.Exercises[0].ExerciseID != "deadlift" || got.Exercises[1].ExerciseID != "row" {
		t.Fatalf("agenda not replaced: %+v", got.Exercises)
	}

	// Re-fetch to confirm persistence.
	re := decodePlan(t, do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, ""), http.StatusOK)
	if len(re.Exercises) != 2 || re.Exercises[0].ExerciseID != "deadlift" {
		t.Errorf("get after update: %+v", re.Exercises)
	}
}

func TestUpdateHandler_KeepsAgendaWhenExercisesAbsent(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC",
		"exercises":[{"exercise_id":"squat","sets":[{"target_reps":5}]}]
	}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", body), http.StatusCreated)

	// No exercises key → keep existing agenda. Just reschedule.
	upd := `{"scheduled_start":"2026-07-03T09:00:00Z","scheduled_end":"2026-07-03T10:00:00Z"}`
	got := decodePlan(t, do(t, repo, nil, "u1", "PUT", "/planned-workouts/"+created.ID, upd), http.StatusOK)
	if len(got.Exercises) != 1 || got.Exercises[0].ExerciseID != "squat" {
		t.Errorf("agenda should be preserved: %+v", got.Exercises)
	}
	if !got.ScheduledStart.Equal(mustTime(t, "2026-07-03T09:00:00Z")) {
		t.Errorf("reschedule not applied: %v", got.ScheduledStart)
	}
}

func TestUpdateHandler_EmptyAgendaClears(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{
		"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC",
		"exercises":[{"exercise_id":"squat","sets":[{"target_reps":5}]}]
	}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", body), http.StatusCreated)

	// Explicit empty array → clear agenda.
	upd := `{"exercises":[]}`
	got := decodePlan(t, do(t, repo, nil, "u1", "PUT", "/planned-workouts/"+created.ID, upd), http.StatusOK)
	if len(got.Exercises) != 0 {
		t.Errorf("agenda should be cleared: %+v", got.Exercises)
	}
}

func TestDeleteHandler(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC"}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", body), http.StatusCreated)

	w := do(t, repo, nil, "u1", "DELETE", "/planned-workouts/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	g := do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, "")
	if g.Code != http.StatusNotFound {
		t.Fatalf("get after delete: got %d want 404", g.Code)
	}
}

func TestSkipHandler(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC"}`
	created := decodePlan(t, do(t, repo, nil, "u1", "POST", "/planned-workouts/", body), http.StatusCreated)

	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/"+created.ID+"/skip", "")
	if w.Code != http.StatusOK {
		t.Fatalf("skip status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	got := decodePlan(t, do(t, repo, nil, "u1", "GET", "/planned-workouts/"+created.ID, ""), http.StatusOK)
	if got.Status != "skipped" {
		t.Errorf("status = %q, want skipped", got.Status)
	}
}

func TestAuthz_CrossUserReturns404(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC"}`
	created := decodePlan(t, do(t, repo, nil, "user-a", "POST", "/planned-workouts/", body), http.StatusCreated)

	cases := []struct {
		name, method, path, body string
	}{
		{"get", "GET", "/planned-workouts/" + created.ID, ""},
		{"put", "PUT", "/planned-workouts/" + created.ID, `{"scheduled_start":"2026-07-02T09:00:00Z","scheduled_end":"2026-07-02T10:00:00Z"}`},
		{"delete", "DELETE", "/planned-workouts/" + created.ID, ""},
		{"skip", "POST", "/planned-workouts/" + created.ID + "/skip", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := do(t, repo, nil, "user-b", c.method, c.path, c.body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestValidation_EndBeforeStart(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T10:00:00Z","scheduled_end":"2026-07-01T09:00:00Z","timezone":"UTC"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestValidation_BadTimezone(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"Mars/Olympus"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestValidation_BadCalendarDetail(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC","calendar_detail":"nope"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateHandler_MissingStartIs400(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC"}`
	w := do(t, repo, nil, "u1", "POST", "/planned-workouts/", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}
