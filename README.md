# Prog Strength API

[![Release and Deploy](https://github.com/Prog-Strength/prog-strength-api/actions/workflows/release.yml/badge.svg?branch=main)](https://github.com/Prog-Strength/prog-strength-api/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/Prog-Strength/prog-strength-api?logo=github&label=release)](https://github.com/Prog-Strength/prog-strength-api/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/Prog-Strength/prog-strength-api?logo=go)](./go.mod)
[![semantic-release](https://img.shields.io/badge/semantic--release-conventional-e10079?logo=semantic-release&logoColor=white)](https://github.com/semantic-release/semantic-release)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)](https://www.conventionalcommits.org)
[![Last commit](https://img.shields.io/github/last-commit/Prog-Strength/prog-strength-api?logo=github)](https://github.com/Prog-Strength/prog-strength-api/commits/main)

The backend service for [Prog Strength](https://api.progstrength.fitness), a
weightlifting tracker that helps lifters see whether their strength is
actually progressing over time. The API owns the exercise catalog, workout
log, bodyweight + nutrition history, and user/auth surface that the web,
mobile, and agent clients build on top of.

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

Prog Strength is a side project focused on a single user problem: *am I
getting stronger?* Workouts are logged as `reps × weight` sets against a
curated, slug-keyed exercise catalog, and clients query that data to render
progressive-overload metrics and dashboards.

The API is intentionally small in scope:

- **Single-user** logging across weightlifting, running (Garmin TCX import),
  nutrition, and bodyweight. AMRAP and distance-based weightlifting sets
  are still deferred — see `AGENTS.md` for the full list.
- **Admin-curated exercise catalog** (no user-created exercises).
- **Cheap single-host deployment** on one EC2 instance.

For the full scope boundary (including what is explicitly *not* in scope),
see [`AGENTS.md`](./AGENTS.md).

## Architecture

```
              ┌──────────────────────────────────────────────┐
              │  api.progstrength.fitness  (Let's Encrypt)   │
              └───────────────────┬──────────────────────────┘
                                  │ HTTPS
                                  ▼
                ┌────────────────────────────────────┐
                │  Caddy  (TLS + reverse proxy)      │
                └─────────────────┬──────────────────┘
                                  │ HTTP, docker network
                                  ▼
        ┌─────────────────────────────────────────────────────┐
        │  api  (Go / Chi, this repo)                         │
        │  ┌───────────────┐  ┌─────────────────────────────┐ │
        │  │ JWT (HS256)   │  │ Domain packages             │ │
        │  │ Google OAuth  │  │ exercise / workout / user / │ │
        │  └───────────────┘  │ bodyweight / nutrition /    │ │
        │                     │ chat / auth / ...           │ │
        │                     └─────────────────────────────┘ │
        └──────────────┬──────────────────────────┬───────────┘
                       │                          │
                       ▼                          ▼
              ┌─────────────────┐         ┌────────────────┐
              │  SQLite         │ ◀────── │  Litestream    │ ──► S3
              │  /data/app.db   │  WAL    │  (sidecar)     │
              └─────────────────┘         └────────────────┘
```

- **Go + Chi router.** Chi was chosen for being minimal — do not replace it
  with a heavier framework.
- **Domain-oriented package layout under `internal/`.** Each domain owns its
  types, repository interface, handler, and errors. There is no top-level
  `models/`, `services/`, or `handlers/` directory and no `pkg/` directory.
- **Repository pattern** for persistence. Every domain defines a
  `Repository` interface with an in-memory implementation today and a
  SQLite implementation as the target.
- **SQLite + Litestream** for storage. The DB file is bind-mounted into the
  container and continuously replicated to S3 with a 24-hour PITR window.
- **JWT (HS256) auth** with Google OAuth as the only identity provider.
  `/exercises` is public; `/workouts` and other user-scoped routes require
  a valid user JWT.
- **Single EC2 host** (Graviton `t4g.small`) fronted by Caddy.
  Infrastructure is provisioned by Terraform in
  [`prog-strength-infra`](https://github.com/Prog-Strength/prog-strength-infra).

A standard envelope (`internal/httpresp/`) wraps every response:

```jsonc
// success
{ "service": "Prog Strength Backend", "message": "...", "data": ... }

// error
{ "service": "Prog Strength Backend", "error": "..." }
```

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

### Run locally (in-memory)

No Docker, no persistence — fastest path for poking at the API.

```bash
go run cmd/api/main.go
```

The server listens on `http://localhost:8080`. State is lost on restart.

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
| GET    | `/workouts`, `/workouts/{id}`| user JWT      | User-scoped workout log.                    |
| POST   | `/workouts`                  | user JWT      | Log a workout (see below).                  |
| GET / POST / PUT | `/bodyweight*`     | user JWT      | Bodyweight history + goals.                 |
| GET / POST | `/nutrition*`            | user JWT      | Timezone-aware daily macro log + goals.     |

Example — log a workout:

```bash
curl -X POST http://localhost:8080/workouts \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Leg Day",
    "performed_at": "2026-05-05T14:00:00Z",
    "notes": "Felt strong today",
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
  }'
```

Weight units are stored **per set** and never converted to a canonical
unit — `225 lb` stays `225 lb` forever, because lifters care about exact
plate math.

## Configuration

All configuration is read from environment variables. Local development
works with everything unset; production uses the secrets listed in
[`DEPLOYMENT.md`](./DEPLOYMENT.md#repository-secrets).

| Variable                | Default            | Purpose                                          |
| ----------------------- | ------------------ | ------------------------------------------------ |
| `DATABASE_URL`          | in-memory          | Path to the SQLite DB file.                      |
| `SERVER_ADDR`           | `:8080`            | HTTP listen address.                             |
| `JWT_SIGNING_KEY`       | —                  | HMAC secret for app JWTs (HS256).                |
| `GOOGLE_CLIENT_ID`      | —                  | OAuth client ID.                                 |
| `GOOGLE_CLIENT_SECRET`  | —                  | OAuth client secret.                             |
| `GOOGLE_REDIRECT_URL`   | —                  | OAuth callback URL.                              |
| `DEV_AUTH`              | `false`            | Gates `POST /auth/dev/token`. Must be `false` in prod. |
| `CORS_ALLOWED_ORIGIN`   | —                  | Comma-separated frontend origins allowed by CORS. Each entry may use a single `*` wildcard (e.g. `https://prog-strength-web-*-<scope>.vercel.app` for Vercel branch previews). |
| `RETURN_TO_ALLOWED_ORIGINS` | —              | OAuth `return_to` allow-list.                    |
| `BETA_ALLOWED_EMAILS`   | —                  | Seed-only: one-time boot seed for the DB-backed beta allow-list (slated for removal). See below. |
| `ADMIN_EMAILS`          | —                  | Comma-separated operator allow-list gating `/admin/beta-emails`. Empty = admin surface disabled (fail-closed). |
| `APP_VERSION`           | `dev`              | Released version, baked in by the Dockerfile.    |

### Beta allowlist

The closed-beta gate (which emails may obtain a JWT at the Google OAuth
callback) is backed by the `beta_allowed_emails` SQLite table, not by an
env var. An **empty table disables the gate** — every authenticated user
gets a token (pre-beta / local dev). Adding an email grants access on that
user's next login; removing one blocks future logins (an already-issued
token lives until it expires — there is no session revocation).

`BETA_ALLOWED_EMAILS` is now **seed-only**: on the first boot where the
table is empty, its comma-separated values are inserted into the table
(`added_by = "seed:BETA_ALLOWED_EMAILS"`) so the live list carries over
with no manual step. After that boot it no longer affects the gate and is
slated for removal in a follow-up.

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

- **Domain packages own their stack.** A package like `internal/workout/`
  contains its types, repository, handler, validation, and errors. New
  surfaces follow the same shape.
- **`Mount(chi.Router)` per domain.** Handlers mount themselves onto the
  router. `internal/server/` owns router construction, graceful shutdown,
  and the health check — and nothing else.
- **Tiny `cmd/api/main.go`.** Signal handling, `server.New()`,
  `server.Run()`. No business logic.
- **Repository interfaces with compile-time assertions.** Every
  implementation is pinned with `var _ Repository = (*MemoryRepository)(nil)`
  so intent is explicit and breaking changes fail at build time.
- **`context.Context` is always the first parameter** on repository
  methods, even when the in-memory implementation does not use it.
- **Soft deletes everywhere** (`DeletedAt *time.Time` with `json:"-"`).
  Read paths filter out deleted rows.
- **Defensive copies in and out of in-memory repos.** Callers never hold
  pointers to internal state.
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
│   ├── workout/             # User-generated workouts: session → exercises → sets.
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
