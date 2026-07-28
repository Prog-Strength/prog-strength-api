# Unified Activity Model — Stage 1 (API) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify lifting workouts and endurance activities into one `activities` base table + per-type detail tables + a Go type registry, per `prog-strength-docs/sows/unified-activity-model.md`, keeping `/workouts/*` endpoints working as shims.

**Architecture:** Class-table inheritance in SQLite (base `activities` + `activity_{run,walk,cycle,other}_details` + strength children `activity_exercises`/`sets`), a `Descriptor`-based type registry in `internal/activity`, and a unified `/activities` HTTP surface. The Go repository *interfaces* stay stable through the schema migration (behavior-preserving), then the registry and unified endpoints layer on top.

**Tech Stack:** Go 1.25, chi, mattn/go-sqlite3, embedded SQL + registered Go migrations (`internal/db`).

**Scope:** prog-strength-api only (SOW rollout stage 1) plus the recipe doc in prog-strength-docs. Stages 2–5 (MCP, web, mobile, shim removal) get their own plans after this lands.

---

## Context for executors (read first)

- Repo: `/Users/jimmywallace/Desktop/prog-strength/repos/prog-strength-api`. Read its `AGENTS.md` / `CLAUDE.md` before coding; use whatever test/lint commands they prescribe. Baseline: `go build ./... && go test ./...`.
- The SOW is the spec: `/Users/jimmywallace/Desktop/prog-strength/repos/prog-strength-docs/sows/unified-activity-model.md`. When this plan and the SOW conflict, the SOW wins; flag the conflict.
- Migrations: `internal/db/migrations/NNN_*.sql`, embedded, applied in numeric order, one transaction each (`internal/db/migrate.go`). Go migrations register in `internal/db/go_migrations.go`. **Check the next free number before creating the migration** (`ls internal/db/migrations | tail`; 042 assumed below — renumber everywhere if taken).
- House conventions: soft delete via `deleted_at`; ownership enforced in repositories; `httpresp` helpers; timezone+local-date on date-windowed endpoints (`internal/daterange`); ids from `internal/id`.
- Every task ends with the FULL suite green (`go test ./...`), not just the package under edit.

### Target schema (reference for all tasks)

```sql
-- activities: one row per training session of ANY type. No CHECK on
-- activity_type / ingest_source: the Go registry is the source of truth
-- (SQLite can't alter a CHECK without a table rebuild).
CREATE TABLE activities (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_type TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    duration_seconds INTEGER,            -- NULL: in-progress lift
    name TEXT,
    notes TEXT,
    avg_heart_rate_bpm INTEGER,
    max_heart_rate_bpm INTEGER,
    total_calories INTEGER,
    ingest_source TEXT NOT NULL,         -- 'manual' | 'manual_tcx' | 'garmin_api' ('whoop' reserved)
    source_activity_id TEXT,             -- NULL for manual entries
    tcx_s3_key TEXT,                     -- NULL unless TCX-derived/enriched
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);
CREATE UNIQUE INDEX idx_activities_dedup ON activities(user_id, ingest_source, source_activity_id)
    WHERE deleted_at IS NULL AND source_activity_id IS NOT NULL;
CREATE INDEX idx_activities_user_start ON activities(user_id, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activities_user_type_start ON activities(user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL;

-- One endurance-shaped detail table per endurance type (accepted duplication;
-- Go shares one store implementation parameterized by table name).
-- Same DDL for activity_walk_details / activity_cycle_details / activity_other_details.
CREATE TABLE activity_run_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);

-- Strength details are 1:N children (renamed from workout_exercises; sets rebuilt
-- because its FK clause names the old table).
CREATE TABLE activity_exercises (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL REFERENCES activities(id) ON DELETE CASCADE,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    exercise_order INTEGER NOT NULL,
    notes TEXT,
    superset_group INTEGER
);
CREATE TABLE sets (  -- rebuilt: column rename workout_exercise_id -> activity_exercise_id
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_exercise_id INTEGER NOT NULL REFERENCES activity_exercises(id) ON DELETE CASCADE,
    reps INTEGER NOT NULL CHECK(reps > 0),
    weight REAL NOT NULL CHECK(weight >= 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb','kg')),
    set_order INTEGER NOT NULL
);
-- personal_records / personal_record_events / exercise_one_rep_max_history:
-- rebuilt with workout_id renamed to activity_id, FK -> activities(id) ON DELETE CASCADE.
-- activity_trackpoints / activity_best_efforts: unchanged.
```

---

### Task 1: Branch setup + plan commit

**Files:** none (git only)

- [ ] **Step 1.1:** In `prog-strength-api`: `git checkout main && git pull --ff-only && git checkout -b feat/unified-activity-model`
- [ ] **Step 1.2:** `git add docs/plans/2026-07-27-unified-activity-model.md && git commit -m "docs: add unified-activity-model stage-1 plan"`

### Task 2: Move `internal/workout` → `internal/activity/strength` (mechanical, zero behavior change)

**Files:**
- Move: every file in `internal/workout/` → `internal/activity/strength/` (package `strength`)
- Modify: all importers — `internal/server/server.go`, `internal/server/timeline_hydrator.go`, `internal/server/timeline_backfill.go`, `internal/server/plan_matcher.go`, `internal/server/profile_stats_sources.go`, plus anything `grep -rln "internal/workout"` finds

- [ ] **Step 2.1:** `git mv internal/workout internal/activity/strength`, change `package workout` → `package strength` in every moved file (including `_test.go`).
- [ ] **Step 2.2:** Update all import paths and qualifiers (`workout.` → `strength.`) across the repo: `grep -rln "internal/workout" --include="*.go" | xargs sed -i '' 's|internal/workout|internal/activity/strength|g'` then fix qualifiers per file (do NOT blind-sed `workout.` — review each site; e.g. `workout.Repository` → `strength.Repository`).
- [ ] **Step 2.3:** Rename exported types only where the package rename makes them stutter-free duplicates (`strength.Workout` stays `Workout` for now — renaming the type is NOT this task).
- [ ] **Step 2.4:** `go build ./... && go test ./...` — everything green with zero test-content edits (import lines in tests excepted).
- [ ] **Step 2.5:** Commit: `refactor: move internal/workout to internal/activity/strength`

**Constraint:** `internal/activity` (parent) must not import `internal/activity/strength` (child) — descriptors get registered from `internal/server` in Task 4.

### Task 3: Migration 042 + behavior-preserving repository rewrite (the centerpiece)

This is one task because the schema and the SQL that reads it must change in the same commit. The Go **interfaces** (`activity.Repository`, `strength.Repository`) and all **endpoint contracts** stay as-is; only SQL, models, and the TCX-attach internals change. The existing test suites are the behavior spec — they must pass with minimal edits (only where they touch removed internals like `ActivityID`).

**Files:**
- Create: `internal/db/migrations/042_unified_activity_model.sql` (full SQL below)
- Create: `internal/db/migrate_upto_test.go` (test-only partial-migration helper)
- Create: `internal/db/migration_042_test.go` (fixture-based data-migration tests)
- Modify: `internal/activity/model.go`, `internal/activity/sqlite_repository.go` (+ its tests where they assert raw SQL/schema)
- Modify: `internal/activity/strength/workout.go`, `sqlite_repository.go`, `handler_tcx.go`, `personal_record_sqlite.go`, `onerepmax.go` (SQL referencing `workouts`/`workout_exercises`/`workout_id`), `user_headline_exercises_sqlite.go` (only if it references moved tables)
- Modify: `internal/server/timeline_backfill.go` (raw SQL reads `FROM workouts`)
- Audit: `grep -rn "FROM workouts\|JOIN workouts\|workout_exercises\|workout_exercise_id\|workout_id" --include="*.go" internal/` — every hit must be visited

- [ ] **Step 3.1: Write the migration.** `internal/db/migrations/042_unified_activity_model.sql`:

```sql
-- migrations/042_unified_activity_model.sql
-- Unify lifting workouts and endurance activities into one base table with
-- per-type detail tables. See prog-strength-docs/sows/unified-activity-model.md.
-- Id preservation is the invariant: workout ids become activity ids verbatim,
-- so PRs, 1RM history, timeline posts, and planned-workout completions never
-- re-point ids — only FK targets are rebuilt.
-- The INSERT of workout rows into the new base uses plain INSERT: a PK
-- collision with an existing activity id aborts the whole transaction, which
-- IS the id-disjointness assertion.

PRAGMA defer_foreign_keys = 1;

-- 1. New base table (no CHECK enums: the Go registry owns valid values).
CREATE TABLE activities_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_type TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    duration_seconds INTEGER,
    name TEXT,
    notes TEXT,
    avg_heart_rate_bpm INTEGER,
    max_heart_rate_bpm INTEGER,
    total_calories INTEGER,
    ingest_source TEXT NOT NULL,
    source_activity_id TEXT,
    tcx_s3_key TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

-- 2. Carry every existing endurance/other activity row EXCEPT strength_training
--    enrichment rows that are the linked target of a workout (those fold into
--    the workout's row in step 3). updated_at seeds from created_at.
INSERT INTO activities_new (
    id, user_id, activity_type, start_time, duration_seconds, name, notes,
    avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
    ingest_source, source_activity_id, tcx_s3_key, created_at, updated_at, deleted_at
)
SELECT id, user_id, activity_type, start_time, duration_seconds, name, NULL,
       avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
       ingest_source, source_activity_id, tcx_s3_key, created_at, created_at, deleted_at
FROM activities
WHERE id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

-- 3. Lift every workout into the base, folding its linked enrichment row
--    (vitals, tcx provenance, duration) when one exists. Ids are reused.
INSERT INTO activities_new (
    id, user_id, activity_type, start_time, duration_seconds, name, notes,
    avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
    ingest_source, source_activity_id, tcx_s3_key, created_at, updated_at, deleted_at
)
SELECT w.id, w.user_id, 'strength_training', w.performed_at,
       CASE
         WHEN a.duration_seconds IS NOT NULL THEN a.duration_seconds
         WHEN w.ended_at IS NOT NULL
           THEN CAST(ROUND((julianday(w.ended_at) - julianday(w.performed_at)) * 86400) AS INTEGER)
         ELSE NULL
       END,
       NULLIF(w.name, ''), NULLIF(w.notes, ''),
       a.avg_heart_rate_bpm, a.max_heart_rate_bpm, a.total_calories,
       COALESCE(a.ingest_source, 'manual'), a.source_activity_id, a.tcx_s3_key,
       w.created_at, w.updated_at, w.deleted_at
FROM workouts w
LEFT JOIN activities a ON a.id = w.activity_id;

-- 4. Re-key folded enrichment trackpoints to the workout's id.
UPDATE activity_trackpoints
SET activity_id = (SELECT w.id FROM workouts w WHERE w.activity_id = activity_trackpoints.activity_id)
WHERE activity_id IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

-- 5. Endurance columns move into per-type detail tables (from the OLD table).
CREATE TABLE activity_run_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities_new(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);
INSERT INTO activity_run_details
SELECT id, distance_meters, raw_distance_meters, avg_pace_sec_per_km,
       best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
FROM activities WHERE activity_type = 'running'
  AND id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);
-- Repeat the CREATE + INSERT verbatim for activity_walk_details ('walking'),
-- activity_cycle_details ('cycling'), activity_other_details ('other').

-- 6. Strength children: rename + re-point. sets is rebuilt because its FK
--    clause names workout_exercises.
CREATE TABLE activity_exercises (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL REFERENCES activities_new(id) ON DELETE CASCADE,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    exercise_order INTEGER NOT NULL,
    notes TEXT,
    superset_group INTEGER
);
INSERT INTO activity_exercises (id, activity_id, exercise_id, exercise_order, notes, superset_group)
SELECT id, workout_id, exercise_id, exercise_order, notes, superset_group FROM workout_exercises;

CREATE TABLE sets_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_exercise_id INTEGER NOT NULL REFERENCES activity_exercises(id) ON DELETE CASCADE,
    reps INTEGER NOT NULL CHECK(reps > 0),
    weight REAL NOT NULL CHECK(weight >= 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb','kg')),
    set_order INTEGER NOT NULL
);
INSERT INTO sets_new (id, activity_exercise_id, reps, weight, unit, set_order)
SELECT id, workout_exercise_id, reps, weight, unit, set_order FROM sets;
DROP TABLE sets;
ALTER TABLE sets_new RENAME TO sets;
DROP TABLE workout_exercises;

-- 7. Re-point strength-stat FKs: workout_id -> activity_id (ids unchanged).
--    Rebuild each of personal_records, personal_record_events,
--    exercise_one_rep_max_history with the SAME columns as today except
--    workout_id renamed activity_id REFERENCES activities_new(id) ON DELETE CASCADE,
--    INSERT ... SELECT all rows, DROP old, RENAME new. (Copy each table's
--    current DDL from migrations 003/004 as the base.)

-- 8. Normalize the planned-workout discriminator (column + CHECK kept; full
--    drop happens in the stage-5 cleanup SOW work).
UPDATE planned_workouts SET completed_session_kind = 'activity'
WHERE completed_session_kind = 'workout';

-- 9. Swap the base table and recreate indexes. timeline_post is untouched.
DROP TABLE workouts;
DROP TABLE activities;
ALTER TABLE activities_new RENAME TO activities;
CREATE UNIQUE INDEX idx_activities_dedup ON activities(user_id, ingest_source, source_activity_id)
    WHERE deleted_at IS NULL AND source_activity_id IS NOT NULL;
CREATE INDEX idx_activities_user_start ON activities(user_id, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activities_user_type_start ON activities(user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL;
```

Notes for the implementer: (a) expand the step-5 "repeat" and step-7 rebuild comments into real SQL — copy current DDL from `001/003/004` migrations plus later ALTERs; (b) `REFERENCES activities_new(...)` clauses survive the final RENAME (SQLite rewrites the FK target name on `ALTER TABLE ... RENAME TO`) — verify with `PRAGMA foreign_key_list(activity_run_details)` in the migration test; (c) after the swap run `PRAGMA foreign_key_check` in the test, not the migration.

- [ ] **Step 3.2: Test-only partial migration helper.** `internal/db/migrate_upto_test.go`:

```go
package db

import "database/sql"

// migrateUpTo applies all registered migrations with Version <= max. Test-only:
// lets migration tests build a fixture at schema N-1, seed rows, then apply N.
func migrateUpTo(sqldb *sql.DB, max int) error {
	ctx := context.Background()
	if err := ensureMigrationsTable(ctx, sqldb); err != nil {
		return err
	}
	migrations, err := collectMigrations(registeredGoMigrations())
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version > max {
			break
		}
		applied, err := isApplied(ctx, sqldb, m.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, sqldb, m); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 3.3: Migration tests (write BEFORE running 042 against anything).** `internal/db/migration_042_test.go`, all subtests sharing one fixture builder: open in-memory sqlite (`sql.Open("sqlite3", ":memory:?_foreign_keys=on")`), `migrateUpTo(db, 41)`, seed with raw INSERTs, then `Migrate(db)` and assert. Fixture rows:
  - user `u1`: workout `w1` (ended, 2 exercises × 2 sets, PR row + PR event + 1RM row referencing it), workout `w2` (no `ended_at`, no enrichment), workout `w3` linked to `strength_training` activity `a3` (vitals + tcx_s3_key + 3 trackpoints), running activity `a1` (full endurance columns + 2 trackpoints + 1 best-effort row), walking activity `a2`, timeline posts for `w1` (`source_type='workout'`) and `a1` (`'run'`), planned workout completed by `w1` (`completed_session_kind='workout'`).
  - Assertions:
    1. `activities` contains `w1,w2,w3,a1,a2` and NOT `a3`; `w1.activity_type='strength_training'`, `start_time == old performed_at`, `duration_seconds == ended-performed` in seconds; `w2.duration_seconds IS NULL`; `w3` carries `a3`'s vitals, `tcx_s3_key`, `ingest_source='manual_tcx'`, `source_activity_id`.
    2. `a3`'s trackpoints now have `activity_id='w3'`; `a1`'s trackpoints untouched.
    3. `activity_run_details` has exactly `a1` with all endurance columns equal to the seed; `activity_walk_details` has `a2`.
    4. `activity_exercises`/`sets` row counts and integer ids preserved; `sets.activity_exercise_id` values equal the old `workout_exercise_id`s.
    5. `personal_records`, `personal_record_events`, `exercise_one_rep_max_history` row-identical except the column is `activity_id`; `PRAGMA foreign_key_check` returns no rows.
    6. `timeline_post` rows byte-identical (select all columns, compare to seed).
    7. `planned_workouts.completed_session_kind = 'activity'`.
    8. Id-collision abort: separate subtest — seed an activities row and a workouts row with the SAME id at schema 041, run `Migrate`, assert it errors and (fresh connection) the old tables still exist (`sqlite_master`) — proves transactionality.
- [ ] **Step 3.4:** Run only these: `go test ./internal/db/ -run Migration042 -v` → must pass. (The rest of the suite is BROKEN at this point — expected; do not commit yet.)
- [ ] **Step 3.5: Rewrite `internal/activity` persistence to the new schema, keeping `activity.Repository` and the `Activity` struct intact** (the struct keeps its endurance fields; they now load from the detail table). Method-by-method mapping:
  - `Create`: INSERT base row (vitals, provenance) + INSERT into the type's detail table (`activity_run_details` etc. via a `detailTable(activityType)` helper) + trackpoints; `updated_at = created_at`.
  - `List`: `SELECT ... FROM activities a LEFT JOIN activity_run_details r ON r.activity_id=a.id LEFT JOIN activity_walk_details w ... LEFT JOIN activity_cycle_details c ... LEFT JOIN activity_other_details o ...` with `COALESCE(r.distance_meters, w.distance_meters, c.distance_meters, o.distance_meters)` per endurance column; keep the existing `WHERE activity_type != 'strength_training'` exclusion for now (removed in Task 5).
  - `ListInRange`, `Get`, `GetBySourceActivityID`, `SummariesByIDs`: same join; `SummariesByIDs` keeps returning strength rows (vitals live on base — no join needed for them).
  - `Rename` → also touch `updated_at`. `Calibrate`/`ChangeEnvironment`: UPDATE moves to the detail table (environment/distance/paces/raw are detail columns now); trackpoint logic unchanged.
  - `RunningMetrics`, `GetUserRunningBestEfforts`, `GetRunningBestEffortHistory`, `ListRunningSamplesSince`, `RecentHRStats`: `JOIN activity_run_details` instead of reading endurance columns off the base; filters stay `activity_type='running'`.
  - `SoftDelete`: unchanged (base only).
- [ ] **Step 3.6: Rewrite `internal/activity/strength` persistence, collapsing the dual-row bridge:**
  - `Workout` struct: **delete `ActivityID`**; keep `EndedAt` as a derived convenience (`start_time + duration_seconds` when duration non-NULL) so handler DTOs don't change; add no new fields.
  - `sqlite_repository.go`: `workouts` → `activities WHERE activity_type='strength_training'`; `performed_at` → `start_time`; `ended_at` → read as `CASE WHEN duration_seconds IS NULL THEN NULL ELSE datetime(start_time, '+'||duration_seconds||' seconds') END` (write side: store `duration_seconds` from `EndedAt−PerformedAt`); `workout_exercises` → `activity_exercises` (`workout_id`→`activity_id`), `sets.workout_exercise_id` → `activity_exercise_id`. `Create` sets `ingest_source='manual'`.
  - `AttachActivity`/`DetachActivity`/`GetByActivityID` are **removed from the interface**. TCX attach (`handler_tcx.go`) becomes single-row: parse TCX → UPDATE the workout's own activities row (vitals, `tcx_s3_key`, `ingest_source='manual_tcx'`, `source_activity_id`, duration when the workout has none) + INSERT trackpoints keyed by the workout id + archive to S3. Detach clears those columns and deletes the trackpoints. The endpoints keep their paths and response shapes; the `activity_id` field in workout DTOs now returns the workout's own id when `tcx_s3_key` is non-NULL, else null (keeps web/mobile rendering logic alive through the shim period).
  - `personal_record_sqlite.go` / `onerepmax.go` / progression SQL: `workout_id` → `activity_id`, joins re-pointed.
- [ ] **Step 3.7:** `internal/server/timeline_backfill.go`: workouts query becomes `SELECT id, user_id, start_time FROM activities WHERE activity_type='strength_training' AND deleted_at IS NULL`; run/best-effort queries add the `activity_run_details` join only if they read endurance columns (they read `start_time` — check).
- [ ] **Step 3.8:** Run the audit grep from the Files list; visit every remaining hit (memory repositories, test fixtures, comments).
- [ ] **Step 3.9:** `go build ./... && go test ./...` — full suite green. Test edits allowed ONLY for removed internals (`ActivityID`, attach/detach repo methods, raw-SQL fixtures naming old tables); any other failing test means behavior drifted — fix the code, not the test.
- [ ] **Step 3.10:** Commit: `feat: migration 042 — unified activity base table with per-type details`

### Task 4: Type registry

**Files:**
- Create: `internal/activity/registry.go`, `internal/activity/registry_test.go`
- Create: `internal/activity/strength/descriptor.go`, `internal/activity/endurance_descriptor.go`
- Modify: `internal/server/server.go` (build + inject the registry)

- [ ] **Step 4.1: Failing tests first** (`registry_test.go`): registering two descriptors then `Lookup("running")` returns the right one; `Lookup("bogus")` returns `ErrUnknownActivityType`; duplicate registration panics (wiring bug, fail fast at boot).
- [ ] **Step 4.2:** `internal/activity/registry.go`:

```go
package activity

// Descriptor is everything the base domain needs to know about one activity
// type. Adding a new type = implement a Descriptor + register it in server.go
// (+ a detail-table migration only if the type has unique structured metrics).
// See prog-strength-docs: "adding an activity type" recipe.
type Descriptor struct {
	Type ActivityType
	// ValidateCreate checks a typed create/update payload before any write.
	ValidateCreate func(req CreateRequest) error
	// Details loads/saves the type's detail representation. Nil for
	// base-only types (e.g. a future kickboxing).
	Details DetailStore
	// Summarize renders the list/card summary for feeds and unified lists.
	Summarize func(a Activity) Summary
	// MountRoutes registers type-specific endpoints (nil when none).
	MountRoutes func(r chi.Router)
}

type DetailStore interface {
	Load(ctx context.Context, activityID string) (any, error)
	Save(ctx context.Context, activityID string, details any) error
	Delete(ctx context.Context, activityID string) error
}

type Registry struct{ byType map[ActivityType]*Descriptor }

func NewRegistry(ds ...*Descriptor) *Registry { /* panics on duplicate Type */ }
func (r *Registry) Lookup(t ActivityType) (*Descriptor, error) // ErrUnknownActivityType
func (r *Registry) Types() []ActivityType                      // sorted, for error messages
```

`CreateRequest` and `Summary` (title, subtitle, metric chips — the shape `server/timeline_hydrator.go` already renders) live in `internal/activity`; reuse the hydrator's existing card vocabulary rather than inventing a second one.
- [ ] **Step 4.3:** Endurance descriptors (`endurance_descriptor.go`): one constructor `NewEnduranceDescriptor(t ActivityType, store DetailStore)` used for running/walking/cycling/other; the shared store is parameterized by detail-table name. `Summarize` for running: distance + pace ("5.0 mi · 41:12" style, mirroring the hydrator's current run card).
- [ ] **Step 4.4:** Strength descriptor (`strength/descriptor.go`): `ValidateCreate` delegates to the existing `Workout.Validate` path; `Details` wraps the exercises/sets load/save the repository already has; `Summarize` mirrors the hydrator's current workout card ("12 sets · 8,400 lb").
- [ ] **Step 4.5:** `server.go`: build `activity.NewRegistry(run, walk, cycle, other, strengthDesc)` and inject it into the activity handler (next task uses it; this task only wires construction).
- [ ] **Step 4.6:** `go test ./...` green. Commit: `feat: activity type registry with endurance + strength descriptors`

### Task 5: Unified `/activities` surface

**Files:**
- Modify: `internal/activity/handler.go` (+ handler tests)
- Modify: `internal/activity/strength/handler.go` (typed create path reuse)
- Modify: `internal/server/server.go` (mount type routes via registry)

- [ ] **Step 5.1: Failing handler tests first:** (a) `GET /activities` now INCLUDES strength rows, each item carrying `activity_type` and a `summary`; (b) `GET /activities?type=running` filters; (c) `GET /activities/{id}` of a strength session returns exercises/sets under `details`; (d) `POST /activities` with `{"activity_type":"strength_training", "start_time":..., "details":{exercises...}}` creates a session (assert via GET); (e) `POST /activities` with unknown type → 422 listing valid types; (f) `PUT`/`DELETE` round-trip for both a run and a lift.
- [ ] **Step 5.2:** Implement: list drops the strength exclusion (repository `List` gains an `includeStrength`/type-filter param — extend the repo method signature, update existing callers); `Get` consults `Registry.Lookup(a.ActivityType).Details` for the typed payload; `POST/PUT` route through `ValidateCreate` + base insert + `Details.Save`; `DELETE` stays soft. Response DTO: base fields + `activity_type` + `summary` (from `Summarize`) on list; + `details` on get. Existing `/activities/tcx`, `/activities/{id}/calibrate`, PATCH rename/environment: unchanged, now mounted via the run descriptor's `MountRoutes`. Strength TCX attach also mounts at `POST|DELETE /activities/{id}/tcx` (keep the `/workouts/{id}/tcx` alias).
- [ ] **Step 5.3:** `/workouts/*` handlers remain mounted unchanged (they are now shims by construction — same store underneath). Add a `// Deprecated: stage-5 cleanup removes these; see unified-activity-model SOW.` comment at the mount site.
- [ ] **Step 5.4:** Add `GET /activities/progression` routed to the existing progression handler (keep `/workouts/progression` alias).
- [ ] **Step 5.5:** `go test ./...` green. Commit: `feat: unified /activities surface over the type registry`

### Task 6: Aggregate surfaces read the unified base

**Files:**
- Modify: `internal/dashboard/handler.go`, `internal/snapshot/aggregate.go` (+ service), `internal/server/timeline_hydrator.go`, `internal/server/profile_stats_sources.go`, `internal/server/plan_matcher.go` (+ each one's tests)

- [ ] **Step 6.1:** Dashboard: replace the separate `workoutRepo.ListByUser` + `activityRepo.ListInRange` fetches with one `activityRepo.ListInRange` over all types; streak/tile logic branches on `activity_type` where it previously branched on source. Existing dashboard tests must pass unchanged (shape-identical response).
- [ ] **Step 6.2:** Snapshot `countActiveDays`: strength + running day sets both come from the one base list; steps unchanged.
- [ ] **Step 6.3:** Timeline hydrator: `hydrateWorkouts`/`hydrateRuns` collapse into one batch fetch from the unified store + `registry.Lookup(type).Summarize`; `source_type` values `'workout'`/`'run'` are retained on posts and both resolve through it. PR/best-effort hydration unchanged.
- [ ] **Step 6.4:** Profile stats sources + plan matchers: read the unified base with type filters; `LiftSessionSource`/`RunningSampleSource` seams keep their interfaces, implementations converge on `activityRepo`.
- [ ] **Step 6.5:** `go test ./...` green. Commit: `refactor: aggregate surfaces read the unified activity base`

### Task 7: Registry contract test (the pattern's guarantee)

**Files:**
- Create: `internal/activity/contract_test.go` (or `internal/server/` if wiring access is needed)

- [ ] **Step 7.1:** Register a fake base-only type `"shadowboxing"` (nil `Details`, trivial `Summarize`) into a test registry + handler + in-memory/sqlite store. Assert, with NO code outside the descriptor: `POST /activities` creates it; it appears in `GET /activities` and type-filtered list with its summary; dashboard streak/active-day logic counts it; timeline `EnsurePost`+hydration renders it; snapshot active-days includes it. This test failing in the future means someone broke the "new types come free" invariant.
- [ ] **Step 7.2:** `go test ./...` green. Commit: `test: registry contract — base-only types flow through all surfaces`

### Task 8: Recipe doc + SOW bookkeeping (prog-strength-docs)

**Files:**
- Create: `prog-strength-docs/adding-an-activity-type.md`
- Modify: `prog-strength-docs/sows/unified-activity-model.md` (note stage-1 landing when merged)

- [ ] **Step 8.1:** Write the recipe: (1) add the `ActivityType` constant; (2) implement a `Descriptor` (reuse `NewEnduranceDescriptor` for endurance-shaped types; nil `Details` for base-only types); (3) only if unique structured metrics: one migration creating `activity_<type>_details` + a `DetailStore`; (4) register in `server.go`; (5) what comes free (unified list/create, timeline, dashboard, snapshot, MCP `log_activity` once stage 2 lands) and what doesn't (type-specific analytics). Include the shadowboxing contract test as the worked example.
- [ ] **Step 8.2:** Commit in the docs repo on a branch (`docs: add adding-an-activity-type recipe`), PR it.

### Final verification (before declaring stage 1 done)

- [ ] `go build ./... && go vet ./... && go test ./...` — all green.
- [ ] Fresh-DB boot check: delete/point at a scratch SQLite file, start the server, confirm all 42 migrations apply and `/activities` + `/workouts` respond.
- [ ] Migrated-DB check: copy a real (or seeded) pre-042 DB, boot against it, spot-check a lift with TCX enrichment renders with HR data via both `GET /workouts/{id}` and `GET /activities/{id}`.
- [ ] The `superpowers:verification-before-completion` skill applies before any "done" claim.

---

## Self-review notes

- Spec coverage: SOW data model → Tasks 3; registry → 4; API surface → 5; aggregates → 6; contract test → 7; recipe doc → 8; shims → 5.3/5.4; MCP/web/mobile/cleanup are explicitly out of stage 1 (SOW rollout stages 2–5).
- Known intentional deviations from bite-size purity: Tasks 3.5/3.6 rewrite existing SQL files against the mapping specs rather than reproducing ~1,700 lines of repository code inline; the existing test suites are the behavior oracle, and 3.9 forbids test edits beyond removed internals.
- `List` signature change (Task 5.2) is the only repo-interface change after Task 3; everything else layers on top.
