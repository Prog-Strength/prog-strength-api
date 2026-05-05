# Progressive Overload Fitness Tracker

## Project purpose

A backend API for weightlifters to track workouts and visualize progressive
overload. The core user problem: a lifter wants to know whether their strength
is actually progressing over time. The app answers that question through
metrics, dashboards, and tables built on top of logged workout data.

This is a side project. The owner is an experienced software engineer (Python
background) who is **new to Go** and using this project to learn the language.
Prefer idiomatic Go and explain non-obvious idioms when introducing them.
Chi was chosen specifically because it's minimal — do not suggest replacing it
with a heavier framework.

## Scope: what this is and isn't

**In scope (v1):**
- Weightlifting workouts only — sets defined as reps × weight.
- Single user logging their own workouts.
- Shared, admin-curated catalog of exercises.
- Cheap single-host deployment (single EC2 instance, containerized).

**Explicitly out of scope until further notice:**
- Cardio, running, timed exercises, distance-based workouts, AMRAP sets.
  The owner is a hybrid athlete and may add these later, but ship weightlifting first.
- User-created exercises. The catalog is curated. Strong typing on enums
  (MuscleGroup, Equipment) reflects this.
- Multi-tenant scaling, horizontal scaling of the API tier.
- Social features, sharing, public profiles.

When tempted to add a feature outside this scope, push back and ask first.

## Architecture decisions (locked in)

These have been debated and decided. Don't relitigate without a strong reason.

- **Domain-oriented package layout**, not layered. Each domain owns its types,
  repository, handler, and errors. No top-level `models/`, `services/`, or
  `handlers/` directories.
- **`internal/` for all application code.** No `pkg/` directory — this is an
  application, not a library.
- **`cmd/api/main.go`** is the only binary entry point. Keep it tiny: signal
  handling + `server.New()` + `server.Run()`.
- **`internal/server/`** owns HTTP infrastructure (router construction,
  graceful shutdown, health check). Domain handlers mount themselves onto
  the router via a `Mount(chi.Router)` method.
- **Repository pattern** for persistence. Each domain defines a `Repository`
  interface and an in-memory implementation. Use compile-time assertions
  (`var _ Repository = (*MemoryRepository)(nil)`) to make intent explicit.
- **Persistence target: SQLite** (with Litestream → S3 for backup). Not yet
  implemented — in-memory repos are the current source of truth. When swapping
  in SQLite, the interface should not need to change.
- **Soft deletes everywhere.** `DeletedAt *time.Time` with `json:"-"`. Filter
  out soft-deleted rows in all read paths.
- **Defensive copies in/out of in-memory repos.** Callers should never hold
  pointers to internal state.
- **`context.Context` first parameter on every repository method.** Even when
  the in-memory implementation doesn't use it.

## Domain model

### Exercise (`internal/exercise/`)

The shared catalog. Read-only from end users; admin-managed (mechanism TBD).

- `Exercise` struct with `ID` (slug, e.g. `"back-squat"`), `Name`, `Description`,
  `MuscleGroups []MuscleGroup`, `Equipment []Equipment`, timestamps, soft delete.
- **Slug IDs, not UUIDs.** Stable, human-readable, never renamed. Workout
  logs reference these IDs forever.
- `MuscleGroup` and `Equipment` are typed-string enums with `Valid()` methods.
  Closed sets — adding a value requires a code change. This is a feature.
- **Bench angles are distinct equipment values** (`flat_bench`, `incline_bench`,
  `decline_bench`), not a property of a generic bench. The angle changes which
  muscles are loaded, so it's part of "what equipment do you need."
- **Primary movers only** in `MuscleGroups`. Don't list every secondary
  contributor; otherwise filtering becomes useless.
- The seeded catalog lives in `catalog.go` as a `var Catalog []Exercise`.
  Validated at test time by `catalog_test.go` (no duplicate IDs, all enums valid).
- `Repository` interface is **read-only** by design. Admin writes, when added,
  belong on a separate interface.

### Workout (`internal/workout/`)

User-generated. Three-level structure:

- `Workout` — a session: `UserID`, `PerformedAt`, `Notes`, `Exercises`, timestamps.
- `WorkoutExercise` — one exercise within a session: `ExerciseID` (references
  `exercise.Exercise.ID` opaquely), `Order` (explicit, not slice-position),
  `Sets`, `Notes`.
- `Set` — `Reps int`, `Weight float64`, `Unit user.WeightUnit`. Bodyweight is
  `Weight: 0`.
- **Weight unit is stored per set, not converted to a canonical unit.** Lifters
  care about exact plate math; round-trip conversion drift is unacceptable.
  `225 lb` should be `225 lb` forever.
- **No referential integrity check inside `Validate()`.** Workout package
  doesn't import exercise package's data, only references its IDs as strings.
  "Does this exercise exist?" is a service-layer concern.

### User (`internal/user/`)

- `User` struct: `ID`, `Email`, `DisplayName`, `WeightUnit`, timestamps, soft delete.
- **`WeightUnit` lives in the user package**, not workout. Workout imports user.
- **Email is immutable through `Update`.** It's the OAuth identifier; changing
  it requires a separate re-verification flow that doesn't exist yet.
- **Email is normalized** (lowercase + trim) on every write and lookup.
- `GetByEmail` exists alongside `GetByID` to support OAuth login lookup.
- No password fields. Auth strategy is OAuth-only (see below).

## Authentication plan (not yet built)

Decided strategy: **OAuth-only via Google**, issuing app-level JWTs after callback.

Reasoning: cost is paramount, target audience has Google accounts, skips all
password-management complexity (hashing, resets, lockouts).

Staging plan:
1. Build user data layer ✅ (done)
2. Build `POST /workouts` with a **dev-only `X-User-ID` header shortcut**.
   Mark it explicitly as dev-only.
3. Add OAuth + JWT middleware. Middleware sets `userID` in request context.
   Handlers switch from reading the header to reading from context.

Libraries: `golang.org/x/oauth2` + `github.com/golang-jwt/jwt/v5`.

## HTTP conventions

- **Handler lives in the domain package** as `handler.go`, exposed as a
  `Handler` struct with `NewHandler(repo Repository)` and `Mount(chi.Router)`.
- **Response envelopes for collections** (`{"data": [...]}`), raw object for
  single resources. Lets pagination be added without breaking changes.
- **Error envelope:** `{"error": "human-readable message"}`. Machine-readable
  error codes will be added when a client needs them — not preemptively.
- **Validate at the boundary.** Reject invalid query params and request bodies
  before calling the repository, with `400 Bad Request` and a clear message.
- `errors.Is(err, ErrNotFound)` — never `err == ErrNotFound`. Repository
  implementations may wrap errors with context.
- Helper functions (`writeJSON`, `writeError`, `writeServerError`) currently
  duplicated per package. Extract to a shared package only after **three**
  duplications, not before.

## Things deliberately NOT done yet

If you're tempted to add any of these, ask first. They've been considered and deferred:

- Structured logging (`log/slog`). Will adopt; not yet.
- `Config` struct / env-var loading. Hardcoded values until there are 3+ knobs.
- DI framework (Wire, fx). Plain constructors only.
- Pagination on `GET /exercises`. Catalog is small enough that returning all
  is correct.
- `UnmarshalJSON` on enum types. Validation happens at the handler boundary.
- Aggregating multi-error validation. First-error-wins for now.
- Machine-readable error codes.
- Transaction support on repositories.
- Admin write endpoints for exercises.
- Email/password authentication.
- Email change flow.
- Secondary muscle groups, exercise variants, set RPE/tempo metadata.

## Code style preferences

- **Prefer concise solutions.** The owner explicitly dislikes verbose code.
  Start simple; add complexity only when justified.
- **One file per type/concept** in domain packages (e.g. `muscle_group.go`,
  `equipment.go`, `exercise.go`). Splitting early is fine — file boundaries
  in Go are free.
- **Comment the *why*, not the *what*.** Use comments for non-obvious design
  choices, especially where idiomatic Go differs from Python (interfaces,
  zero values, defensive copies).
- **No emoji in code or comments.** No decorative ASCII art.
- **Tests live next to implementation** (`foo.go` / `foo_test.go`).

## Current project layout

```
.
├── cmd/api/main.go              # Entry point — keep tiny
├── go.mod
├── go.sum
└── internal
    ├── exercise
    │   ├── catalog.go           # Seeded var Catalog []Exercise
    │   ├── catalog_test.go      # Validates catalog at test time
    │   ├── equipment.go         # Equipment enum + Valid()
    │   ├── errors.go            # Sentinel errors + InvalidEnumError
    │   ├── exercise.go          # Exercise struct + Validate()
    │   ├── handler.go           # GET /exercises, GET /exercises/{id}
    │   ├── memory_repository.go # In-memory Repository impl
    │   ├── muscle_group.go      # MuscleGroup enum + Valid()
    │   └── repository.go        # Read-only Repository interface
    ├── server
    │   ├── health.go
    │   └── server.go            # New(), Run(), graceful shutdown
    ├── user
    │   ├── errors.go
    │   ├── memory_repository.go
    │   ├── repository.go
    │   ├── user.go
    │   └── weight_unit.go       # WeightUnit lives here, not in workout
    └── workout
        ├── errors.go
        ├── memory_repository.go
        ├── repository.go
        ├── set.go               # imports user.WeightUnit
        ├── workout.go
        └── workout_exercise.go
```

Note: handler.go has only been written for `exercise/`. `workout/` and `user/`
handlers are not yet built.

## When in doubt

- Ask before adding a third-party dependency.
- Ask before adding a feature that's listed in "Things deliberately NOT done."
- Ask before changing one of the locked-in architecture decisions.
- Default to small, reviewable changes over sweeping refactors.
