# Running max-effort estimation engine (`internal/running/estimate`)

## What it does

Predicts the time an athlete would run **if they raced a standard distance today**.
Distinct from logged best efforts (historical fact) and from demographic tables alone
(forward-looking model output). Pure package — no HTTP/DB imports; `EstimateInput.Now`
is injected for deterministic back-tests.

Product context: [`running-max-effort-estimates.md`](../../../../prog-strength-docs/sows/running-max-effort-estimates.md).
v2 refactor: [`running-max-effort-estimation-v2.md`](../../../../prog-strength-docs/sows/running-max-effort-estimation-v2.md).

## Version history

| Version | Summary |
| --- | --- |
| `1.0.0` | Shipped Bayesian Riegel fit; full best-effort history as evidence; no logged-best floor. |
| `2.0.0` | Anchor + filtered history evidence; pace/HR quality weights; logged-best floor; age/sex standards; demographics wired from profile. |

**Bump `EstimatorVersion` on any behavioral change** (constants, weighting, floor, standards).

## The model

Log-space Riegel power law: `y = β0 + β1·x` with `x = ln(distance)`, `y = ln(time)`.
Weighted Bayesian linear regression with diagonal prior; conjugate Gaussian posterior
(closed-form 2×2 invert). Prediction at target distance; asymmetric lognormal band.

v2 keeps this core; changes are **evidence selection**, **weights**, **floor**, and **standards**.

## Evidence policy

Implemented in `activity.assembleAttempts` (handler), helpers in `evidence.go`:

1. **Anchors** — one row per distance: current best from `GetUserRunningBestEfforts`,
   marked `IsCurrentBestAtDistance`.
2. **Supporting history** — other rows at the same distance only if within
   `HistoryMaxGapPct` (3%) of the anchor duration. Stale slower PRs are excluded.
3. **Cross-distance** — anchors at other distances always participate (quality-weighted).

## Weighting signals

Combined weight: `w_recency · w_distance_ratio · w_pace_ratio · w_hr_intensity · w_anchor_boost`

| Factor | File | Role |
| --- | --- | --- |
| Recency | `weighting.go` | `exp(-Δt / tauDays)`, default τ=180d |
| Distance ratio | `weighting.go` | effort / activity distance (v1 heuristic) |
| Pace ratio | `weighting.go` | window pace vs activity avg; >1.0 down-weights |
| HR intensity | handler | fraction of window in zones 4–5; nil → neutral |
| Anchor boost | `riegel_bayes.go` | ×2.0 on current-best rows |

Tune when estimates feel too conservative (raise anchor boost, tighten history gap) or
too aggressive (widen gap, strengthen pace/HR down-weight).

## Invariants

**Logged-best floor:** when `LoggedBestSeconds` is set, post-fit
`Seconds ≤ logged best` and `LowerSeconds ≤ logged best`. Sets
`FlooredAtLoggedBest` and `Basis = logged_best_floor` when the raw model was slower.
`RawSeconds` preserves the pre-floor projection for UI footnotes.

A max-effort estimate must never be **slower** than a time the athlete has already run
at that distance.

## Demographics

`DemographicsFromProfile(height_cm, birthdate, sex, now)` — age from ISO birthdate;
`demographicLevelPrior` requires **sex**; age refines the 5K reference via embedded
age-band table (`standards.go`, WMA-style weak priors). Missing fields widen toward data-only fit.

## Inputs & outputs

See `estimator.go` for `Attempt`, `EstimateInput`, `EstimateResult`. Notable v2 fields:
`ActivityAvgPaceSecPerKm`, window bounds, `HRZoneHighIntensityPct`, `IsCurrentBestAtDistance`,
`LoggedBestSeconds`, `RawSeconds`, `FlooredAtLoggedBest`.

## Evaluation fixtures

```bash
go test ./internal/running/estimate/...
```

| Test | Asserts |
| --- | --- |
| `TestFixture_Owner5KMismatch` | 5K estimate ≤ logged 23:06 with mixed history |
| `TestFixture_MileFloor` | Mile estimate ≤ logged 7:20 |
| `TestLoggedBestFloor_Clamp` | Floor math |
| `TestIncludeSupportingHistory_ExcludesStalePRs` | 3% gap rule |

## How to iterate

| Constant | Default | Role |
| --- | --- | --- |
| `HistoryMaxGapPct` | 0.03 | supporting history inclusion |
| `anchorBoost` | 2.0 | anchor weight multiplier |
| `priorBeta1Mean` | 1.06 | Riegel exponent prior |
| `priorBeta1Var` | 0.0025 | slope tightness |
| `tauDays` | 180 | recency |
| `paceRatioKnee` | 1.15 | pace down-weight start |
| `hrIntensityFullPct` | 0.25 | HR fraction for full weight |

Checklist before merge: update fixtures, bump `EstimatorVersion`, run
`go test ./internal/running/estimate/...`, spot-check handler tests.
