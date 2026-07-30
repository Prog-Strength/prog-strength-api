# Prog Strength API

[![Release and Deploy](https://github.com/Prog-Strength/prog-strength-api/actions/workflows/release.yml/badge.svg?branch=main)](https://github.com/Prog-Strength/prog-strength-api/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/Prog-Strength/prog-strength-api?logo=github&label=release)](https://github.com/Prog-Strength/prog-strength-api/releases)
[![Last commit](https://img.shields.io/github/last-commit/Prog-Strength/prog-strength-api?logo=github)](https://github.com/Prog-Strength/prog-strength-api/commits/main)
[![Test coverage](https://codecov.io/gh/Prog-Strength/prog-strength-api/branch/main/graph/badge.svg)](https://codecov.io/gh/Prog-Strength/prog-strength-api)

The backend service for [Prog Strength](https://api.progstrength.fitness), a
fitness tracker that pulls training, nutrition, and biometric data out of the
apps and wearables it already lives in and organizes it into one queryable
history. The API owns the exercise catalog, activity log, nutrition and
bodyweight history, wearable integrations, social graph, and the structured
data surface the Prog Strength AI agent reads — the web, mobile, and agent
clients all build on top of it.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Quick Start](#quick-start)
- [API Surface](#api-surface)
- [Configuration](#configuration)
- [Coding Practices](#coding-practices)
- [Project Structure](#project-structure)
- [Release & Deployment](#release--deployment)
- [Related Repositories](#related-repositories)
- [Further Reading](#further-reading)

## Overview

Prog Strength began as an answer to one question — *am I getting stronger?* —
and grew into the system of record for everything that answer depends on.
Training load, recovery, nutrition, bodyweight, and daily movement all land in
one place under a shared schema, so they can be read together instead of one
app at a time.

The aim is to integrate with as many wearables and data sources as possible,
ingest as much biometric telemetry as they will give up, and turn it into
something legible: dashboards and timelines for the user, and structured,
queryable history for an AI agent that helps shape programs, dial in
nutrition, and keep the whole picture in view.

### What it tracks

- **Strength training.** Sets logged as `reps × weight` against a curated,
  slug-keyed exercise catalog, with personal-record detection, estimated-1RM
  progression history, and planned workouts you can schedule, calibrate,
  complete, or skip.
- **Endurance activities.** Running, walking, and cycling under one
  sport-agnostic activity model, with Garmin TCX file import, best efforts
  and max effort by distance, and heart-rate-zone time accumulation.
- **Nutrition.** Daily macro logging backed by a food lookup service, so
  hitting a macro target doesn't mean hand-entering every gram.
- **Bodyweight and steps.** Bodyweight history and daily step counts, either
  entered directly or batched in from a connected source.
- **Wearables and connections.** Whoop via OAuth plus webhooks for recovery
  and activity data, Google Calendar sync for scheduled activity, and Garmin
  through TCX import. New sources plug into the same activity taxonomy rather
  than getting a bespoke pipeline.
- **A social layer.** Follow requests, public profiles with stats, user
  search, and a timeline feed with comments and reactions.
- **The AI agent surface.** Chat sessions, messages, turns, and tool-call
  telemetry; a vector memory store the agent writes to and retrieves from;
  and a usage ledger enforcing a daily spend cap.

Dashboard and timeline endpoints assemble these into per-day summaries so
clients don't have to fan out across every domain to render a screen.

### Still intentionally narrow

- **Admin-curated exercise catalog** (no user-created exercises).
- **OAuth-only auth**, currently gated behind a closed-beta email allowlist.
- **Cheap single-host deployment** on one EC2 instance — one SQLite file,
  Litestream to S3. Multi-tenant or horizontally-scaled deployment is out.

For the full scope boundary (including what is explicitly *not* in scope),
see [`AGENTS.md`](./AGENTS.md).

## Architecture

```
      Garmin device                              Whoop cloud
    (.tcx export file)                        (OAuth + webhooks)
            │                                         │
            ▼                                         │
Web client / Mobile client                            │
            │                                         │
            │ HTTPS                                   │ HTTPS
            ▼                                         ▼
 ┌──────────────────────────────────────────────────────────────────┐
 │  api.progstrength.fitness   (Let's Encrypt)                      │
 │  Caddy — TLS + reverse proxy; refuses to proxy /internal/*       │
 └──────────┬─────────────────────────────────────────┬─────────────┘
            │ /chat                                   │ REST
════════════╪═════════════════════════════════════════╪══════════════  single EC2 host
            ▼                                         ▼
 ┌──────────────────────┐    tool-use    ┌──────────────────────────┐
 │ agent  (FastAPI)     │───────────────▶│ mcp  (FastMCP)           │
 │ prog-strength-agent  │                │ prog-strength-mcp        │
 │ natural-language     │                │ wraps API endpoints as   │
 │ entry point          │                │ agent-callable tools     │
 └──────────┬───────────┘                └────────────┬─────────────┘
            │ telemetry writes                        │ REST
            │ /internal/telemetry/*                   │
            ▼                                         ▼
 ┌──────────────────────────────────────────────────────────────────┐
 │  api   (Go / Chi — this repo)                                    │
 │  JWT HS256 · Google OAuth · closed-beta allowlist gate           │
 │  domain packages under internal/: activity, exercise,            │
 │  nutrition, bodyweight, steps, timeline, follow, chat,           │
 │  vectormemory, whoop*, calendar*, usage, ...                     │
 └────────┬─────────────────────┬───────────────────────┬───────────┘
          │                     │                       │
          ▼                     ▼                       ▼
 ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────────┐
 │ SQLite           │  │ Litestream       │  │ External APIs        │
 │  app.db          │◀─│  (sidecar)       │  │ Google OAuth ·       │
 │  telemetry.db    │  │  reads WAL       │  │ Google Calendar ·    │
 └──────────────────┘  └────────┬─────────┘  │ Whoop · FatSecret ·  │
                                │            │ USDA · OpenAI ·      │
                                ▼            │ Anthropic            │
                       ┌──────────────────┐  └──────────────────────┘
                       │ S3               │
                       │  db replica      │
                       │  avatars, TCX    │
                       └──────────────────┘
```

### External dependencies

Everything the API talks to that it does not own. Credentials for each are
supplied via environment (see [Configuration](#configuration)).

| Service                    | Used for                                                   |
| -------------------------- | ---------------------------------------------------------- |
| **Google OAuth**           | The only sign-in identity provider; issues the app JWT.     |
| **Google Calendar**        | Two-way sync of scheduled activity via a connected account. |
| **Whoop**                  | OAuth-connected recovery and activity data, plus inbound webhooks. |
| **Garmin**                 | No API — `.tcx` files exported from the device are uploaded and parsed. |
| **FatSecret**              | Food and macro lookup for nutrition logging.                |
| **USDA FoodData Central**  | Second nutrition lookup source.                             |
| **OpenAI**                 | Embeddings for the vector memory store; TTS priced in the usage ledger. |
| **Anthropic**              | Distills raw chat history into durable agent memories.      |
| **AWS S3**                 | Litestream replica target, plus avatar and TCX object storage. |

- **Go + Chi router.** Chi was chosen for being minimal — do not replace it
  with a heavier framework.
- **Domain-oriented package layout under `internal/`.** Each domain owns its
  types, repository interface, handler, and errors. There is no top-level
  `models/`, `services/`, or `handlers/` directory and no `pkg/` directory.
- **Repository pattern** for persistence. Every domain defines a
  `Repository` interface with a single SQLite implementation.
- **SQLite + Litestream** for storage. The DB file is bind-mounted into the
  container and continuously replicated to S3 with a 24-hour PITR window.
- **JWT (HS256) auth** with Google OAuth as the only identity provider.
  `/exercises` is public; `/activities` and other user-scoped routes require
  a valid user JWT.
- **The MCP is the agent's only tool boundary.** New agent-facing
  capabilities are added as MCP tools wrapping API endpoints, never as
  direct API calls from the agent.
- **`/internal/*` is network-scoped, not token-scoped.** Caddy refuses to
  proxy it, so only container-to-container traffic on the Docker network
  reaches the agent telemetry routes. The single-host network boundary *is*
  the auth boundary for that surface.
- **Single EC2 host** (Graviton `t4g.small`) fronted by Caddy. Every service
  in the diagram runs as a container on that one box.

A standard envelope (`internal/httpresp/`) wraps every response. Success and
error shapes are deliberately disjoint — `message` only ever appears on
success, `error` only on failure — so a client can tell them apart without
consulting the status code:

```jsonc
// success
{
  "service": "Prog Strength Backend",
  "version": "0.83.2",
  "request_id": "01JZ8M4K7QW2R9V3XN6ABCD012",
  "message": "...",
  "data": {}                    // omitted when the handler returns no payload
}
```

```jsonc
// error
{
  "service": "Prog Strength Backend",
  "version": "0.83.2",
  "request_id": "01JZ8M4K7QW2R9V3XN6ABCD012",
  "error": "...",
  "code": "tcx_not_running"     // omitted unless the handler opts in
}
```

- **`request_id`** echoes the per-request correlation id minted by the
  `requestid` middleware, and is also returned on the `X-Request-ID` header.
  It is omitted outside the HTTP stack (background jobs), where no request
  id exists to echo.
- **`code`** is a machine-readable error identifier for clients that branch
  on a precise reason rather than parsing prose — the TCX import flow uses
  `tcx_not_running`, `file_too_large`, `duplicate_run`. Handlers opt in via
  `ErrorWithCode`; plain `Error` responses omit the field entirely.

For a deeper dive on the host itself — Terraform modules, the Caddy config,
DNS, TLS, container wiring, and backup topology — see
[`prog-strength-infra`](https://github.com/Prog-Strength/prog-strength-infra).
The sibling services are covered in
[Related Repositories](#related-repositories).

## Tech Stack

| Layer            | Choice                                                      |
| ---------------- | ----------------------------------------------------------- |
| Language         | Go 1.25                                                     |
| HTTP router      | [`go-chi/chi`](https://github.com/go-chi/chi)               |
| Auth             | Google OAuth → app-issued HS256 JWT (`golang-jwt/jwt/v5`)   |
| Storage          | SQLite (`mattn/go-sqlite3`) + Litestream → S3               |
| Metrics          | Prometheus client + Grafana (via infra repo)                |
| Container        | Docker (multi-stage), linux/arm64 image in ECR              |
| Reverse proxy    | Caddy (TLS via Let's Encrypt)                               |
| CI / CD          | GitHub Actions + semantic-release + conventional commits    |
| Host             | Single EC2 `t4g.small` (Graviton, Ubuntu 24.04)             |

## Quick Start

### Run locally

`DATABASE_URL` is required; point it at a SQLite file (created on first
run) — fastest path for poking at the API.

```bash
DATABASE_URL=./dev.db JWT_SIGNING_KEY=local-dev-do-not-ship go run ./cmd/api
```

The server listens on `http://localhost:8080`. State persists in the SQLite file at `DATABASE_URL`, so it survives restarts.

### Run locally (Docker + SQLite)

```bash
docker compose up -d            # build + start
docker compose logs -f api      # tail logs
docker compose down             # stop
```

State persists to `./data/app.db`.

### Build

```bash
go build ./...
docker build -t prog-strength-api .
```

### Test

```bash
go test ./...
```

Tests live next to the code they exercise (`foo.go` / `foo_test.go`).

## API Surface

| Method | Path                         | Auth          | Notes                                       |
| ------ | ---------------------------- | ------------- | ------------------------------------------- |
| GET    | `/health`                    | none          | Liveness probe.                             |
| GET    | `/exercises`                 | none          | Full catalog. No pagination by design.      |
| GET    | `/exercises/{id}`            | none          | Slug-keyed (e.g. `barbell-high-bar-back-squat`). |
| GET    | `/me`                        | user JWT      | The authed user.                            |
| GET    | `/activities`, `/activities/{id}`| user JWT  | Unified session log — every type incl. strength. |
| POST / PUT / DELETE | `/activities*`  | user JWT      | Typed create/update/delete (see below).     |
| GET / POST / PUT | `/bodyweight*`     | user JWT      | Bodyweight history + goals.                 |
| GET / POST | `/nutrition*`            | user JWT      | Timezone-aware daily macro log + goals.     |

The legacy `/workouts*` surface was removed once web, mobile, and MCP migrated
to `/activities` (unified-activity-model SOW, stage 5). A lift is now an
`activity_type: "strength_training"` session posted to `/activities`.

Example — log a workout:

```bash
curl -X POST http://localhost:8080/activities \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "activity_type": "strength_training",
    "name": "Leg Day",
    "start_time": "2026-05-05T14:00:00Z",
    "notes": "Felt strong today",
    "details": {
      "exercises": [
        {
          "exercise_id": "barbell-high-bar-back-squat",
          "notes": "Good depth",
          "sets": [
            {"reps": 5, "weight": 135, "unit": "lb"},
            {"reps": 5, "weight": 185, "unit": "lb"},
            {"reps": 5, "weight": 225, "unit": "lb"}
          ]
        }
      ]
    }
  }'
```

Weight units are stored **per set** and never converted to a canonical
unit — `225 lb` stays `225 lb` forever, because lifters care about exact
plate math.

## Configuration

All configuration is read from environment variables. Local development
requires `DATABASE_URL`; the rest can be left unset. Production uses the
secrets listed in [`DEPLOYMENT.md`](./DEPLOYMENT.md#repository-secrets).

| Variable                | Default            | Purpose                                          |
| ----------------------- | ------------------ | ------------------------------------------------ |
| `DATABASE_URL`          | (required)         | Path to the SQLite DB file. Required — no default. |
| `SERVER_ADDR`           | `:8080`            | HTTP listen address.                             |
| `JWT_SIGNING_KEY`       | —                  | HMAC secret for app JWTs (HS256).                |
| `GOOGLE_CLIENT_ID`      | —                  | OAuth client ID.                                 |
| `GOOGLE_CLIENT_SECRET`  | —                  | OAuth client secret.                             |
| `GOOGLE_REDIRECT_URL`   | —                  | OAuth callback URL.                              |
| `DEV_AUTH`              | `false`            | Gates `POST /auth/dev/token`. Must be `false` in prod. |
| `CORS_ALLOWED_ORIGIN`   | —                  | Comma-separated frontend origins allowed by CORS. Each entry may use a single `*` wildcard (e.g. `https://prog-strength-web-*-<scope>.vercel.app` for Vercel branch previews). |
| `RETURN_TO_ALLOWED_ORIGINS` | —              | OAuth `return_to` allow-list.                    |
| `ADMIN_EMAILS`          | —                  | Comma-separated operator allow-list gating `/admin/beta-emails`. Empty = admin surface disabled (fail-closed). |
| `APP_VERSION`           | `dev`              | Released version, baked in by the Dockerfile.    |

### Beta allowlist

The closed-beta gate (which emails may obtain a JWT at the Google OAuth
callback) is backed by the `beta_allowed_emails` SQLite table, not by an
env var. An **empty table disables the gate** — every authenticated user
gets a token (pre-beta / local dev). Adding an email grants access on that
user's next login; removing one blocks future logins (an already-issued
token lives until it expires — there is no session revocation).

The list is managed entirely at runtime through the admin endpoints below;
there is no env-var seed. (Earlier releases carried the list over from a
`BETA_ALLOWED_EMAILS` secret on first boot — that seed has been removed now
that the table is the system of record.)

Operators manage the list at runtime via three admin endpoints, all behind
`RequireUser` + an admin gate (the caller's email must be in `ADMIN_EMAILS`;
an empty `ADMIN_EMAILS` makes the whole surface return `403`). Admin calls
use the operator's ordinary user JWT — no separate token.

| Method & path                       | Behavior                                                                 |
| ----------------------------------- | ------------------------------------------------------------------------ |
| `GET /admin/beta-emails`            | List entries (`email`, `added_at`, `added_by`, `note`), sorted by `added_at` asc. |
| `POST /admin/beta-emails`           | Body `{ "email": "...", "note": "optional" }`. `201` when added, `200` if already present (idempotent), `400` on a malformed/empty email. `added_by` is the calling admin's email. |
| `DELETE /admin/beta-emails/{email}` | `204` on removal, `404` if the email was not on the list.                 |

Non-admin callers get `403` on every verb.

## Coding Practices

The repository follows a small set of locked-in conventions. The
authoritative reference is [`AGENTS.md`](./AGENTS.md) (with
[`CONTRIBUTING.md`](./CONTRIBUTING.md) covering the contribution
workflow itself); the highlights:

- **Domain packages own their stack.** A package like
  `internal/activity/strength/` contains its types, repository, handler,
  validation, and errors. New surfaces follow the same shape.
- **`Mount(chi.Router)` per domain.** Handlers mount themselves onto the
  router. `internal/server/` owns router construction, graceful shutdown,
  and the health check — and nothing else.
- **Tiny `cmd/api/main.go`.** Signal handling, `server.New()`,
  `server.Run()`. No business logic.
- **Repository interfaces with compile-time assertions.** Every
  implementation is pinned with `var _ Repository = (*SQLiteRepository)(nil)`
  so intent is explicit and breaking changes fail at build time.
- **`context.Context` is always the first parameter** on repository
  methods, so cancellation and deadlines propagate to every query.
- **Soft deletes everywhere** (`DeletedAt *time.Time` with `json:"-"`).
  Read paths filter out deleted rows.
- **Slug IDs, not UUIDs**, for the exercise catalog. They are stable,
  human-readable, and referenced by workout logs forever.
- **Closed enums** for `MuscleGroup` and `Equipment` with `Valid()`
  methods. Adding a value requires a code change — this is deliberate.
- **Validate at the boundary.** Reject bad input at the handler with
  `400 Bad Request` before reaching the repository. First-error-wins.
- **`errors.Is(err, ErrNotFound)`**, never `==`. Repositories are free to
  wrap errors with context.
- **No emoji in code or comments, no decorative ASCII art.**
- **Comment the *why*, not the *what*.** Especially where idiomatic Go
  differs from Python.
- **Conventional Commits.** Commit type drives the release — only
  `feat:` and `fix:` cut a new version (see below).

A short list of things we have deliberately *not* built yet (DI framework,
structured logging, multi-error aggregation, admin write endpoints, etc.)
lives in [`AGENTS.md`](./AGENTS.md#deliberately-deferred).
Please ask before adding any of them.

## Project Structure

```
.
├── cmd/api/                 # The one and only binary entry point.
├── internal/
│   ├── server/              # Router construction, graceful shutdown, /health.
│   ├── config/              # Env-var loading.
│   ├── auth/                # Google OAuth + JWT issue/verify, middleware.
│   ├── user/                # User domain (owns WeightUnit).
│   ├── exercise/            # Admin-curated, slug-keyed catalog (read-only).
│   ├── activity/            # Unified activity domain: runs/walks/rides, TCX ingest.
│   │   └── strength/        # Lifting workouts: session → exercises → sets.
│   ├── bodyweight/          # Bodyweight history + goals.
│   ├── nutrition/           # Timezone-aware daily macro log + goals.
│   ├── chat/                # Agent intent classification persistence.
│   ├── db/                  # SQLite plumbing.
│   ├── httpresp/            # Shared success / error response envelope.
│   ├── telemetry/           # Prometheus metrics.
│   ├── id/                  # ID generation helpers.
│   ├── version/             # Embedded APP_VERSION accessor.
│   └── testutil/            # Shared test helpers.
├── data/                    # Local SQLite DB (gitignored in prod paths).
├── Dockerfile               # Multi-stage; bakes APP_VERSION via -ldflags.
├── CHANGELOG.md             # Auto-generated by semantic-release.
├── AGENTS.md                # Authoritative architecture + style guide (for any agent).
├── CONTRIBUTING.md          # Contribution workflow: pre-commit, conventional commits, CI.
└── DEPLOYMENT.md            # Host layout, secrets, manual ops, troubleshooting.
```

## Release & Deployment

Releases are fully automated.

1. Push to `main` with a [Conventional Commit](https://www.conventionalcommits.org)
   message.
2. `.github/workflows/release.yml` runs
   [semantic-release](https://github.com/semantic-release/semantic-release):
   - `feat:` → minor bump, `fix:` → patch bump.
   - `chore:` / `docs:` / `refactor:` / `test:` → no release, no deploy.
   - Tag, changelog, and GitHub Release are created automatically.
3. The release pipeline then builds a `linux/arm64` Docker image on a
   GitHub-hosted ARM runner, pushes it to ECR under the released tag, and
   SSHes into the EC2 host to roll the running stack onto the new image.

A `Manual Deploy` workflow (`workflow_dispatch`) is also available for
rolling a fresh host onto the latest released tag without manufacturing a
fake commit.

Full host layout, secret list, manual operations, and troubleshooting all
live in [`DEPLOYMENT.md`](./DEPLOYMENT.md).

## Related Repositories

Prog Strength is split across a small set of sibling repos:

| Repo                                                                              | Role                                                          |
| --------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| [`prog-strength-api`](https://github.com/Prog-Strength/prog-strength-api)         | This repo — the Go backend.                                   |
| [`prog-strength-mcp`](https://github.com/Prog-Strength/prog-strength-mcp)         | FastMCP server that proxies the API for agent tool-use.       |
| [`prog-strength-agent`](https://github.com/Prog-Strength/prog-strength-agent)     | FastAPI agent service; natural-language entry point.          |
| [`prog-strength-web`](https://github.com/Prog-Strength/prog-strength-web)         | Web frontend.                                                 |
| [`prog-strength-mobile`](https://github.com/Prog-Strength/prog-strength-mobile)   | Mobile client.                                                |
| [`prog-strength-infra`](https://github.com/Prog-Strength/prog-strength-infra)     | Terraform + Caddy; provisions the shared EC2 host.            |
| [`prog-strength-organization`](https://github.com/Prog-Strength/prog-strength-organization) | Org-level config.                                  |

The MCP is the boundary between the agent and this API — new agent-facing
capabilities should be added as MCP tools that wrap API endpoints, not as
direct API calls from the agent.

## Further Reading

- [`AGENTS.md`](./AGENTS.md) — architecture decisions, domain model, scope,
  coding style. Authoritative reference for human and AI contributors.
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — contribution workflow:
  pre-commit setup, conventional commits, CI checks, what to do when a
  check fails.
- [`DEPLOYMENT.md`](./DEPLOYMENT.md) — host layout, secrets, manual ops,
  troubleshooting, backup/restore.
- [`CHANGELOG.md`](./CHANGELOG.md) — generated by semantic-release on
  every release.
