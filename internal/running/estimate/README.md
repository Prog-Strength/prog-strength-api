# Running max-effort estimation engine (`internal/running/estimate`)

## What it does

Given an athlete's history of running efforts at various distances (plus
optional demographics), this package predicts the time they would run **if
they raced a standard distance today** — e.g. a projected marathon time. This
is a *forward-looking model output* and is deliberately distinct from the
historical best efforts the activity layer extracts: a best effort is the
fastest window the athlete has *already* run; an estimate is what the model
believes they are *currently capable of* at a distance they may never have
raced. The package is pure and self-contained — it imports nothing from the
HTTP, DB, or activity layers and reads its clock only from `EstimateInput.Now`,
so it is trivially unit-testable and back-testable.

## The model (v1: `riegelBayes`)

The engine fits the classic **Riegel power law** `T = a · D^b` — the empirical
rule that race time scales with distance raised to a fatigue exponent `b`. In
log-space this is a straight line:

```
y = β0 + β1·x        where x = ln(distance), y = ln(time)
```

so `β1` *is* the fatigue exponent and `β0` sets the athlete's overall level.
We fit `(β0, β1)` with a **weighted Bayesian linear regression**:

- **Prior** `β ~ N(m0, S0)`, diagonal. The slope prior is pinned tight at
  `β1 = 1.06` (Riegel's exponent, variance `0.0025`, sd `0.05`) — this is the
  conservatism knob that stops one short effort from implying an absurd
  long-distance time. The level prior `β0` comes from a demographic standard
  when available (moderate variance `0.25`); otherwise it is seeded from the
  fastest effort and given a diffuse variance (`100`) so the data sets the
  level.
- **Likelihood**: each usable effort `i` contributes precision
  `w_i / obsVar`, where `w_i = recencyWeight · qualityWeight` down-weights old
  and sub-maximal efforts and `obsVar` (sd `0.03`, ~3% log-time noise) is the
  fixed observation noise.
- **Posterior**: because the model is conjugate Gaussian, the posterior is
  closed-form. We accumulate the 2×2 normal equations
  `A = S0⁻¹ + Σ (w_i/obsVar)·φ_iφ_iᵀ`, `b = S0⁻¹·m0 + Σ (w_i/obsVar)·φ_i·y_i`
  (with `φ = [1, x]`), **invert the 2×2 by hand**, and get `S_N = A⁻¹`,
  `m_N = S_N·b`. No solver, no iteration — fully deterministic.

Prediction at `x* = ln(target)`: `yhat = m_N·φ*`, predictive variance
`v = φ*ᵀ S_N φ* + obsVar`. The point estimate is `exp(yhat)` and the band is
`exp(yhat ± bandZ·√v)`. Because the math lives in log-time, the band is an
**asymmetric lognormal** interval in seconds — the slow tail is longer, which
is the correct shape for race times.

## Inputs & outputs

**`EstimateInput`**
| Field | Meaning |
|---|---|
| `TargetDistanceKey` / `TargetDistanceMeters` | which standard distance to predict |
| `Attempts` | efforts at **all** distances (not just the target — multi-distance evidence is what fits the slope) |
| `Demographics` | optional age/sex/weight/height; missing fields widen the prior |
| `Now` | injected clock for deterministic recency weighting and back-testing |

**`Attempt`** carries `DistanceMeters`, `DurationSeconds`, `AchievedAt`, and
`ActivityDistanceMeters` (the total distance of the run the effort came from;
`0` = unknown). An attempt is *usable* only if duration and distance are both
positive.

**`EstimateResult`**
| Field | Meaning |
|---|---|
| `Seconds` | point prediction for the target |
| `LowerSeconds` / `UpperSeconds` | asymmetric ~68% (one σ) band |
| `Basis` | which evidence regime produced it (below) |
| `Confidence` | `low` / `medium` / `high`, derived from band width |
| `NPoints` / `NDistances` | usable effort count and distinct-distance count |
| `Version` | `EstimatorVersion`, stamped on every result |

**Basis states** (priority order):
- `insufficient_data` — no usable efforts and no demographic anchor (`Seconds = 0`).
- `demographic_prior` — no efforts, but age+sex give a population standard.
- `single_effort` — usable efforts all at one distance (slope leans on the prior).
- `fitted_curve` — usable efforts span ≥2 distances (slope is genuinely fit).

**Confidence** is derived from the relative half-width
`h = (Upper − Lower) / (2·Seconds)`: `h ≤ 0.04` → `high`, `h ≤ 0.10` →
`medium`, else `low`.

## Assumptions & known limitations

- **Best efforts may be sub-maximal.** An effort is the fastest *window* inside
  a run, which is often not an all-out race. We can only partially correct for
  this (see quality heuristic).
- **Quality heuristic is distance-ratio only (v1).** `qualityWeight` infers
  "was this a real race effort?" purely from `effort / activity` distance. A
  *pace-ratio* refinement (was the window actually run hard relative to the rest
  of the run?) is a planned fast follow.
- **Demographics are weak today.** The standards table is keyed on age + sex,
  but those aren't persisted yet — only height is available. So the
  `demographic_prior` path runs on a deliberately minimal **placeholder** table
  (`standards.go`) whose only job is to make the path real and testable. Do not
  read clinical meaning into its numbers.
- **Fixed observation noise.** `obsVar` is a single constant; the model does
  not yet learn per-athlete or per-distance noise.

## How to iterate

The whole model is a handful of named constants. **Any change to these — or to
the standards table, weighting, or basis/confidence logic — is a behavioral
change and requires bumping `EstimatorVersion`** (`estimator.go`), because that
string is stamped on cached/labeled output downstream.

| Constant | File | Role |
|---|---|---|
| `priorBeta1Mean` (1.06) | `riegel_bayes.go` | Riegel fatigue exponent; the conservatism knob |
| `priorBeta1Var` (0.0025) | `riegel_bayes.go` | slope prior tightness (sd 0.05) — how much multi-distance evidence it takes to move the exponent |
| `priorBeta0Var` (0.25) | `riegel_bayes.go` | level prior variance when a demographic standard anchors it |
| `diffuseBeta0Var` (100.0) | `riegel_bayes.go` | level prior variance when there is no standard (v1 default) |
| `tauDays` (180.0) | `riegel_bayes.go` | recency e-folding constant (weight = 1/e at this age) |
| `obsVar` (0.0009) | `riegel_bayes.go` | observation noise in log-time (sd 0.03 ≈ ~3%) |
| `bandZ` (1.0) | `riegel_bayes.go` | band half-width in σ (~68%) |
| `qualityFloor` (0.25) | `weighting.go` | minimum weight for a tiny window of a long run |
| `qualityKnee` (0.9) | `weighting.go` | effort/activity ratio at/above which an effort earns full weight |

**To add a demographic factor**, extend `demographicLevelPrior` in
`standards.go`: it returns `(m_beta0, variance, ok)` and currently keys on
age + sex with an optional height refinement. Replace the placeholder table
with calibrated standards (and widen the `ok` condition as new demographic
fields are persisted). Keep the level conversion `β0 = ln(T_std) − β1·ln(D_ref)`
so the standard stays consistent with the model's slope.
