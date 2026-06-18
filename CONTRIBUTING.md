# Contributing to prog-strength-api

This is the backend of the Prog Strength stack and is the load-bearing
repo of the project — the API contract every other service (frontend,
MCP, agent) depends on. The bar for changes is correspondingly high.
This document is the source of truth for how to make a change land
cleanly: how to set up your machine, how branching and releases work,
which checks run where, and what's expected of the commits you push.

If you're an AI coding agent making a contribution here, also read
[AGENTS.md](AGENTS.md). It has the project-shaped context (domain
model, architecture decisions, in-flight scope, what NOT to touch)
that this document deliberately omits.

## Table of contents

- [Quick start](#quick-start)
- [Branching and release flow](#branching-and-release-flow)
- [Conventional commits](#conventional-commits)
- [Pre-commit hooks](#pre-commit-hooks)
- [CI checks](#ci-checks)
- [Tests](#tests)
- [Running the API locally](#running-the-api-locally)
- [What to do when checks fail](#what-to-do-when-checks-fail)

## Quick start

```bash
# Clone
git clone git@github.com:Prog-Strength/prog-strength-api.git
cd prog-strength-api

# Install the Go toolchain version the repo expects (read from go.mod).
# Any modern install method works — gvm, mise, asdf, the official tarball,
# or `brew install go`. CI uses go-version-file: go.mod so your local
# version should match.
go version

# Install local lint + format tools (versions match CI).
brew install golangci-lint pre-commit gitleaks
go install golang.org/x/tools/cmd/goimports@latest

# Install the pre-commit hooks (commit-msg + pre-commit + pre-push).
pre-commit install --install-hooks
pre-commit install --hook-type commit-msg
pre-commit install --hook-type pre-push

# Sanity-check
go build ./...
go test ./...
```

You're ready. Branch from `main`, commit with a conventional prefix,
open a PR — the rest of this document explains why each step matters
and where to look when something rejects your change.

## Branching and release flow

```
feature branch ──▶ PR ──▶ squash-merge into main ──▶ semantic-release ──▶ tag + deploy
```

- **`main` is the release branch.** Every push to `main` runs
  [`release.yml`](.github/workflows/release.yml) which calls
  [semantic-release](https://github.com/semantic-release/semantic-release).
  It scans every commit since the last tag, computes the next semver,
  generates the `CHANGELOG.md` entry, tags the repo, builds the image,
  and SSHes into the prod EC2 host to deploy it.

- **Direct pushes to `main` are not allowed.** All changes go through a
  pull request so the CI gate (see below) runs and the conventional
  commit prefix is validated against the PR title before it gets
  squashed onto `main`.

- **Squash-merge is the default merge strategy.** The PR title becomes
  the squash commit subject on `main`. That subject is what
  semantic-release reads — so the PR title's prefix determines whether
  a release is cut and at what version. A `feat:` PR cuts a minor
  release; a `fix:` cuts a patch; a `chore:` / `docs:` / `test:` cuts
  no release at all (the merge lands on `main` but no tag is produced).

- **Branch names.** Loose conventions, not enforced: `feat/<slug>`,
  `fix/<slug>`, `chore/<slug>`. The PR title carries the actual
  conventional prefix; the branch name is for human navigation.

- **Manual deploy as escape hatch.** [`manual-deploy.yml`](.github/workflows/manual-deploy.yml)
  re-deploys the current `main` tag without going through the release
  step. Use it when a deploy failed mid-flight after a release was
  already tagged, not as a routine path.

## Conventional commits

Every PR title (and therefore every squash commit on `main`) must use a
[Conventional Commits](https://www.conventionalcommits.org/) prefix.
This is enforced two ways: locally via the `conventional-pre-commit`
hook on every commit message, and in CI via
[`amannn/action-semantic-pull-request`](https://github.com/amannn/action-semantic-pull-request)
on the PR title.

**Prefixes and release impact:**

| Prefix | Release | When to use |
|--------|---------|------------|
| `feat:` | minor (e.g. `0.38.0 → 0.39.0`) | A new user-visible capability, endpoint, or schema field |
| `fix:` | patch (e.g. `0.38.0 → 0.38.1`) | A bug fix in shipped behavior |
| `perf:` | patch | A performance improvement with no behavior change |
| `refactor:` | none | Code reshape with no behavior change |
| `docs:` | none | README, AGENTS.md, code comments |
| `test:` | none | Test-only changes |
| `chore:` | none | Build files, deps, tooling, this kind of thing |
| `ci:` | none | `.github/workflows/`, pre-commit config |
| `build:` | none | Dockerfile, go.mod manipulation, compile-time concerns |
| `style:` | none | Formatting, whitespace, comment rewording |
| `revert:` | matches the reverted commit | `git revert` body |

**Scope (optional, parens):** the package name when relevant —
`feat(running): add elevation outlier filter`, `fix(httpresp): omit empty
code field`. Free-form; not enforced as a closed list. Cross-cutting
changes can use a high-level scope like `(api)` or `(deploy)`.

**Subject:** lowercase, no trailing period, imperative mood. The CI hook
will reject titles that start uppercase.

**Breaking changes:** put `BREAKING CHANGE: <description>` in the
commit body (NOT the subject). semantic-release will cut a major
release. Don't ship a `feat!:` or `fix!:` either — convention is the
footer, and the rest of this repo's history follows that.

**Examples (good):**

```
feat(running): pace outlier filter + date-range listing
fix(deploy): forward TCX_BUCKET_NAME into prod .env
chore(release): 0.38.1
docs(agents): rewrite for current codebase state
```

**Examples (bad):**

```
Add new feature                       ← no prefix
feat: Add Pace Filter                  ← uppercase subject
Updates                                ← no prefix, no subject of value
update tests for running               ← no prefix; should be `test(running):`
```

## Pre-commit hooks

Configured in [`.pre-commit-config.yaml`](.pre-commit-config.yaml). Two
stages:

**On every commit (`pre-commit` stage):**

- **Generic file hygiene** — trailing whitespace, end-of-file newline,
  YAML/JSON validity, no unresolved merge markers, no checked-in private
  keys, no files larger than 1 MB.
- **Secret scanning** — [gitleaks](https://github.com/gitleaks/gitleaks)
  refuses commits that contain AWS access keys, JWT signing secrets, or
  OAuth client secrets.
- **Go formatting** — `gofmt` and `goimports` rewrite changed `.go`
  files in place. `goimports` uses
  `-local github.com/jwallace145/progressive-overload-fitness-tracker`
  so the module's own packages cluster in a separate import group.

**On `git push` (`pre-push` stage):**

- **`go vet ./...`**
- **`golangci-lint run`** — see [`.golangci.yml`](.golangci.yml) for the
  enabled set.
- **`go mod tidy` drift check** — fails if running `tidy` would change
  `go.mod` or `go.sum`.
- **`go test ./...`** — full suite, no race detector locally (CI runs
  `-race` for that).

The pre-push hooks duplicate what CI runs. They exist so you don't
publish a branch only to discover a CI rejection 90 seconds later. They
do not replace CI, which is the authoritative gate.

**Bypassing.** Don't. If a hook is wrong, fix the hook (and post the
diagnosis on the PR). `git commit --no-verify` exists but should be a
last resort, not a workflow.

## CI checks

Configured in [`.github/workflows/ci.yml`](.github/workflows/ci.yml).
Every PR runs four parallel jobs:

| Job | What it does | Maps to |
|-----|--------------|---------|
| `pr-title` | Validates the PR title is a conventional commit | `conventional-pre-commit` on `commit-msg` |
| `lint` | `go build`, `go vet`, `golangci-lint v2.12.2`, `go mod tidy` drift | `pre-push` Go hooks |
| `test` | `go test -race -cover ./...` (race detector is CI-only) | `pre-push` `go test` (extended) |
| `vulnerabilities` | `govulncheck ./...` against the module's reachable import graph | CI-only |

All four must be green before a PR is mergeable. Branch protection is
configured to require them on `main`.

## Tests

- **Tests live next to implementation** — `foo.go` and `foo_test.go` in
  the same package.
- **`go test ./...`** locally; CI runs `go test -race -cover ./...`.
- **The race detector** catches data races that the normal suite can't.
  If a new test fails only under `-race`, your code has a real bug — do
  not paper over it with mutexes you don't understand.
- **Coverage** is printed per-package in CI. There is no enforced
  threshold yet, but coverage that drops sharply on a PR is a signal
  worth explaining in the description.
- **TDD is the default workflow.** Most domain features in this repo
  were built test-first. See `internal/requestid/requestid_test.go` and
  `internal/httpresp/response_test.go` for the small-package pattern;
  `internal/running/handler_test.go` for the multipart-upload HTTP test
  pattern.

## Running the API locally

Minimal local run requires `DATABASE_URL` pointing at a SQLite file
(created on first run):

```bash
DATABASE_URL=./dev.db JWT_SIGNING_KEY=local-dev-do-not-ship go run ./cmd/api
```

Tests don't need this: they get their own ephemeral SQLite DB via the
`internal/db/dbtest` helper (`dbtest.New(t)`).

For more realistic local dev (SQLite, dev-auth, beta gate, OAuth, S3
TCX archival), see [`README.md`](README.md) — that's the high-level
project doc. This file is about the contribution flow.

## What to do when checks fail

| Failure | Fix |
|---------|-----|
| `pr-title` rejects | Edit the PR title in the GitHub UI to a conventional prefix. Re-runs automatically. |
| `lint` fails on `golangci-lint` | Run `golangci-lint run --fix` locally. Many findings auto-fix; the rest require a code change. |
| `lint` fails on `go mod tidy drift` | Run `go mod tidy` and commit the changed `go.mod` / `go.sum`. |
| `test` fails on race detector | Real race in your change. Find it via `go test -race -run TestName ./pkg`. Do not silence with locks you can't justify. |
| `vulnerabilities` reports a CVE | Read the report. Bump the offending dep, or — if the unsafe path is unreached — annotate with a comment explaining why. Do not silence. |
| Pre-commit hook fails locally on a fresh clone | Make sure `pre-commit install --install-hooks` ran. Confirm `golangci-lint version` is v2.x (the repo config requires v2). |

If a check is failing for what looks like an infrastructure reason
(transient GitHub Actions outage, mirror flake), re-run the job from
the PR's checks tab before assuming the code is the problem.
