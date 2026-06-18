# Prog Strength API — Agent Contributor Guide

This file is for AI coding agents (Claude, Copilot, Codex, Gemini, etc.)
making contributions to `prog-strength-api`. Human contributors should
start with [CONTRIBUTING.md](CONTRIBUTING.md) and [README.md](README.md).

Both will benefit from this file — it's the project-shaped context that
takes longest to recover from code alone: which decisions have been
debated and settled, what's deliberately out of scope, how the domain
model is shaped, and which patterns to follow when adding new code.

This file used to be `CLAUDE.md`. It was renamed to `AGENTS.md` because
the guidance applies to any agent contributor — there's nothing in here
that's Claude-specific.

## What this project is

A single-user fitness tracking backend: the load-bearing service in the
Prog Strength stack. Other repos (`prog-strength-web`,
`prog-strength-mcp`, `prog-strength-agent`) depend on the API contract
defined here. README.md has the high-level architecture overview and
the visual diagram; this file does not duplicate it.

The owner is an experienced engineer with a Python background, using
this project to learn idiomatic Go. Prefer idiomatic Go and explain
non-obvious idioms when introducing them. Chi was chosen specifically
because it's minimal — do not suggest replacing it with a heavier
framework.

## Working on this repo as an agent

A few rules of engagement that have come up enough to be worth stating
up-front:

- **Default to TDD.** The recent additions in this repo
  (`internal/requestid/`, `internal/httpresp/response_test.go`) were
  written test-first. New behavior changes follow the same pattern —
  see CONTRIBUTING.md → Tests for what's expected.
- **Run CI's checks locally BEFORE authoring a PR.** `go vet` and
  `go test` alone are NOT enough — CI also runs golangci-lint (a
  pinned **v2.x**; see `.github/workflows/ci.yml` for the exact
  version) with `gosec`, shadow checking, and more, plus a
  `go mod tidy` drift check and govulncheck. The git hooks that run
  these locally are **per-clone opt-in** and a fresh clone has none
  installed, so first arm them, then verify:

  ```
  pre-commit install --install-hooks --hook-type pre-commit \
      --hook-type commit-msg --hook-type pre-push
  go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<CI-pinned version> run
  go test ./...
  ```

  A PR whose checks you never ran locally is a CI round-trip someone
  else pays for.
- **PR titles are conventional commits, lowercase subject.** A check
  rejects anything else (`feat: nutrition lookup endpoint`, not
  `Nutrition lookup endpoint`). Same prefixes as commit messages:
  semantic-release derives versions from them.
- **Don't relitigate the decisions** in
  [Architecture decisions](#architecture-decisions) below. They were
  debated; the shape is settled. Add features within it.
- **Don't add features listed in
  [Deliberately deferred](#deliberately-deferred).** Ask first.
- **Match the existing style.** Short comments explain *why*, never
  *what*. No emoji or decorative ASCII. One type/concept per file in
  domain packages.
- **Don't add third-party dependencies without asking.** The existing
  set (chi, oauth2, jwt, sqlite3, prometheus, aws-sdk-go-v2) is
  intentionally small.
- **Surface bypasses.** If you find yourself about to use `--no-verify`,
  add a `//nolint:`, silence a `gosec` finding, or skip a test, that's
  a red flag — explain why in the PR rather than hiding it.
- **Stay scoped.** Bug fixes do not need "while I'm here" refactors. A
  one-shot operation does not need a helper. Three similar lines beat
  one premature abstraction.

## Domain model — what's actually here

The original version of this file said the project was "weightlifting
only" with cardio, running, and nutrition deferred. That is no longer
true. The reality:

### Shipping domains (`internal/<name>/`)

- **`workout`** — sessions, exercises within sessions, sets. Three-level
  structure: `Workout` → `WorkoutExercise` → `Set`. Weight stored per
  set with the user's unit (`lb` or `kg`), never converted. Includes
  one-rep-max history backfill and personal-record tracking.
- **`exercise`** — admin-curated catalog of slug-IDed exercises
  (`barbell-high-bar-back-squat`) with closed `MuscleGroup` and
  `Equipment` enums. Read-only from end users. Catalog defined as
  `var Catalog []Exercise` in `catalog.go`; synced into SQLite on
  startup.
- **`user`** — email-keyed users with display name and preferred
  `WeightUnit`. Email is immutable; changing it requires a re-verify
  flow that doesn't exist. Soft-deleted.
- **`auth`** — Google OAuth + HS256 JWT, mounted at `/auth`.
  `RequireUser(secret)` is the middleware that gates user-scoped routes;
  `RequireAdmin(users, ADMIN_EMAILS)` gates the admin surface. Beta gate
  via the DB-backed `internal/beta` allowlist (table `beta_allowed_emails`,
  managed at `/admin/beta-emails`). Dev-auth backdoor (`POST /auth/dev/token`)
  gated by `DEV_AUTH=true`.
- **`running`** — TCX import (Garmin), session CRUD, downsampled
  trackpoints, dashboard metrics. Raw TCX archived via the `Archiver`
  interface (S3 in prod, in-memory in dev). Dedup on
  `(user_id, garmin_activity_id)`.
- **`nutrition`** — daily macro log + goals, pantry items. Timezone-
  aware date contract on read paths (the boundary between user-local
  and UTC is at the handler).
- **`bodyweight`** — weight entries and goals.
- **`chat`** — persistent agent chat sessions: user-scoped CRUD plus
  per-turn append. Persists `last_intent` from agent responses for
  observability.
- **`telemetry`** — agent intent/response event logging. Lives in a
  *separate* SQLite database (`TELEMETRY_DATABASE_URL`) so the high-
  volume agent writes don't share locks or Litestream backups with the
  application data.

### Infrastructure packages

These exist but rarely need changes. Read first; ask before modifying:

- **`server`** — router construction, middleware stack, health check,
  graceful shutdown.
- **`db`** — SQLite open + migration runner. Migrations live in
  `internal/db/migrations/`, embedded via `embed.FS`, numbered
  `001_…_*.sql` through `014_…_*.sql`. Add the next one as `015_*.sql`.
- **`config`** — env-var loading. Don't sprinkle `os.Getenv` calls
  through handlers.
- **`httpresp`** — the standard response envelope. See
  [HTTP conventions](#http-conventions).
- **`requestid`** — per-request correlation ID middleware. See
  [HTTP conventions](#http-conventions).
- **`id`** — 32-char hex ID generator. `id.New()` is the entry point.
- **`version`** — `APP_VERSION` baked in at build time via Dockerfile
  `-ldflags`. Read it via `version.Version`.

### Deliberately deferred

Considered and not on the roadmap. Push back / ask before adding any of
these:

- User-created exercises. Catalog stays curated.
- Multi-tenant or horizontally-scaled deployment. Single host, single
  SQLite file, Litestream backup. Cost is paramount.
- Social features (sharing, public profiles, leaderboards).
- Email/password authentication. OAuth-only.
- Email change flow.
- Aggregated multi-error validation (`first-error-wins` for now).
- DI framework (Wire, fx). Plain constructors.
- Structured logging (`log/slog`). The whole codebase uses `log.Printf`;
  migration is planned but not started.

## Architecture decisions

These have been debated and locked in. Don't relitigate without a real
reason; raise it in a separate discussion before implementing.

### Layout

- **Domain-oriented package layout**, not layered. Each domain owns its
  types, repository, handler, and errors. No top-level `models/`,
  `services/`, or `handlers/`.
- **`internal/` for all application code.** No `pkg/` — this is an
  application, not a library.
- **`cmd/api/main.go`** is the only binary entry point and is kept
  tiny: signal handling + `server.New()` + `server.Run()`.
- **One file per type/concept** in domain packages. File boundaries are
  free in Go; split early.

### Persistence

- **SQLite is the persistence target**, with Litestream → S3 for
  continuous replication. Both are wired up and running in prod.
- **Repository pattern** with a single SQLite implementation per
  domain. Compile-time assertions
  (`var _ Repository = (*SQLiteRepository)(nil)`) keep intent explicit
  and catch interface drift at build time. Tests run against an
  ephemeral SQLite DB via `internal/db/dbtest` (`dbtest.New(t)`).
- **Migrations** are forward-only, embedded via `embed.FS`, applied on
  startup by `db.Migrate(database)`. `schema_migrations` tracks the
  applied set. Highest current: `014_running_dedup_live_only.sql`.
- **Soft deletes everywhere.** `DeletedAt *time.Time` with `json:"-"`.
  All read paths filter them out.
- **`context.Context` first parameter on every repository method.**
  Cancellation and deadlines propagate to every query.
- **User scoping at the storage layer.** Repo methods take a `userID`
  and treat cross-user IDs as `ErrNotFound`. Handlers trust this and
  don't re-check.
- **`errors.Is(err, ErrNotFound)`** — never `err == ErrNotFound`.
  Implementations wrap errors with context.

### HTTP conventions

- **Standard response envelope** in `internal/httpresp/`. Every handler
  uses it; do not hand-roll JSON.
  - Success: `{service, version, request_id, message, data}` (`data`
    omitempty).
  - Error: `{service, version, request_id, error, code}` (`code`
    omitempty).
  - HTTP status code is the success/failure signal; the body explains.
  - Helpers: `OK`, `Created`, `Error`, `ErrorWithCode`,
    `ErrorWithCodeData`, `ServerError`.
- **`request_id`** comes from `internal/requestid`. The middleware
  generates a fresh ID (or accepts the inbound `X-Request-ID` header),
  sets the response header, and seeds the context. httpresp helpers
  read the header. For log lines, use `requestid.FromContext(ctx)`.
- **Machine-readable `code`** is opt-in via `ErrorWithCode`. Used by
  the running TCX import (`tcx_not_running`, `file_too_large`,
  `duplicate_run`, `storage_failed`, etc.). Add codes when a client
  needs to branch on the precise reason — not preemptively.
- **Middleware stack** in `internal/server/server.go`, top to bottom:
  1. `requestid.Middleware` (replaces chi's `middleware.RequestID` —
     chi's only seeded context; this version also sets the response
     header so the frontend can read the id).
  2. `middleware.Logger` (chi).
  3. `middleware.Recoverer` (chi).
  4. `MetricsMiddleware` (project-local Prometheus instrumentation;
     after Recoverer so panics are counted).
  5. `cors.Handler` (conditional on `CORS_ALLOWED_ORIGIN`; PATCH is
     allowed because `/chat-sessions/{id}` does in-place title updates).
- **All `r.Use()` calls must come before any route registration.** Chi
  panics at startup if a route precedes a middleware install. There's
  an explicit boundary comment in `server.go`.
- **Validate at the boundary.** Handlers reject invalid bodies and
  query params with `400` and a clear message before reaching the repo.
- **Handler signature.** Live in the domain package as `handler.go`.
  Expose `Handler` struct with `NewHandler(deps...)` and
  `Mount(chi.Router)`.

### Auth and config

- **OAuth-only via Google.** No password fields, no email/password
  endpoints.
- **JWT (HS256)** issued after the OAuth callback. `JWT_SIGNING_KEY` is
  required at startup; the process fails to boot without it.
- **Dev-auth backdoor** (`POST /auth/dev/token`) gated by
  `DEV_AUTH=true`. Mints a JWT for any email; local-dev only.
- **Beta gate** via the `internal/beta` allowlist, stored in the
  `beta_allowed_emails` table (migration `021`). Users outside the list
  complete OAuth and get a user row but are redirected with
  `#error=beta_required` rather than a token; an empty table opens the gate.
  Operators manage the list at runtime through `/admin/beta-emails`
  (`GET`/`POST`/`DELETE`), gated by `ADMIN_EMAILS` (empty = fail-closed,
  all `403`). `BETA_ALLOWED_EMAILS` is now seed-only — it one-time-seeds the
  table on first boot and is slated for removal.
- **Config** lives in `internal/config/`. README.md has the full env-var
  table with defaults; do not duplicate it here.

## Cross-cutting types worth knowing

These show up across packages and are easy to miss when you're new:

- **`auth.UserIDFrom(ctx) (string, bool)`** — read the authed user ID
  inside a JWT-gated handler. The `bool` is paranoia against missing
  middleware; treat `false` as a server error and bail out.
- **`requestid.FromContext(ctx) string`** — per-request correlation ID
  for log lines. Returns empty when called outside an HTTP request
  (e.g. startup goroutines).
- **`httpresp.*` helpers** — the only legal way to write a response
  body. If you find yourself reaching for `json.NewEncoder(w).Encode`
  in a handler, stop and use the envelope.
- **`id.New()`** — 32-char hex generator. Entity IDs and request IDs
  both use it.
- **`user.WeightUnit`** — lives in the `user` package because it's a
  user preference, not a workout property. `workout` imports `user`,
  never the other way around.

## Code style

- **Comments explain *why*, not *what*.** Idiomatic Go reads itself.
  Reserve comments for non-obvious design choices and surprises that a
  future reader would otherwise have to re-derive.
- **No emoji or decorative ASCII** in code, comments, or commit
  messages.
- **Tests live next to implementation** (`foo.go` / `foo_test.go`).
- **Prefer concise solutions.** The owner explicitly dislikes verbose
  code; start simple, add complexity only when justified by a concrete
  current need.
- **First-error-wins validation** at handler boundaries. No aggregating
  multi-error responses yet.

## When in doubt

- Ask before adding a third-party dependency.
- Ask before adding a feature listed in
  [Deliberately deferred](#deliberately-deferred).
- Ask before changing one of the [Architecture decisions](#architecture-decisions).
- Default to small, reviewable changes over sweeping refactors.
- Default to TDD (see [CONTRIBUTING.md](CONTRIBUTING.md) → Tests).
- If you're about to bypass a pre-commit hook, golangci-lint rule, or
  CI check, that's a red flag — write up why in the PR rather than
  silencing it.
