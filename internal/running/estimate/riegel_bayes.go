package estimate

import "math"

// riegel_bayes.go is the v1 model: a weighted Bayesian linear regression in
// log-space. It treats the classic Riegel power law T = a·D^b as a straight
// line in (ln D, ln T) — y = β0 + β1·x — and fits (β0, β1) by combining a
// physiologically-motivated prior with the athlete's own efforts.
//
// Why Bayesian rather than ordinary least squares: a runner often has one or
// two efforts at a single distance, which OLS cannot fit a slope to at all
// (and over-fits wildly with two). The prior pins the fatigue exponent β1
// near Riegel's 1.06 and only lets consistent multi-distance evidence pull it
// away, so a single 5K never implies an absurd marathon time. The conjugate
// Gaussian form gives a closed-form 2x2 posterior — no solver, no iteration,
// fully deterministic.

// Tunable knobs. Each is a deliberate modeling choice; see README's constants
// table for roles and how to iterate. Changing any of these is a behavioral
// change and requires bumping EstimatorVersion.
const (
	priorBeta1Mean  = 1.06   // Riegel fatigue exponent; the conservatism knob
	priorBeta1Var   = 0.0025 // tight (sd 0.05): takes consistent multi-distance evidence to move it
	priorBeta0Var   = 0.25   // moderate, used when a demographic standard anchors the level
	diffuseBeta0Var = 100.0  // diffuse, used when no demographic standard (v1 default)
	tauDays         = 180.0  // recency half-life-ish constant
	obsVar          = 0.0009 // observation noise in log-time (sd 0.03 ≈ ~3% time error)
	bandZ           = 1.0    // ≈68% (one σ) displayed band
	qualityFloor    = 0.25   // min quality weight for a tiny window of a long run
	qualityKnee     = 0.9    // effort/activity distance ratio at/above which weight is full
)

// riegelBayes is the v1 Estimator. It carries no state — every call is a pure
// function of its input — so the zero value is the usable model.
type riegelBayes struct{}

// usableAttempt is an effort that survived the validity filter, with its log
// coordinates and combined likelihood weight precomputed.
type usableAttempt struct {
	x      float64 // ln(distance meters)
	y      float64 // ln(duration seconds)
	weight float64 // recency * quality
}

// Estimate runs the full pipeline: filter efforts, build the prior (from
// demographics or a data-seeded fallback), fold in the weighted likelihood,
// invert the 2x2 to get the posterior, and predict at the target distance.
func (riegelBayes) Estimate(in EstimateInput) EstimateResult {
	// Filter to usable efforts and precompute their log coordinates + weights.
	used := make([]usableAttempt, 0, len(in.Attempts))
	distinct := map[string]struct{}{}
	for _, a := range in.Attempts {
		if a.DurationSeconds <= 0 || a.DistanceMeters <= 0 {
			continue
		}
		w := recencyWeight(in.Now, a.AchievedAt) * qualityWeight(a.DistanceMeters, a.ActivityDistanceMeters)
		used = append(used, usableAttempt{
			x:      math.Log(a.DistanceMeters),
			y:      math.Log(a.DurationSeconds),
			weight: w,
		})
		// Count distinct distances by the rounded meters value so two efforts
		// at "5k" (5000 m) collapse, but 5k and 10k do not.
		distinct[distanceBucket(a.DistanceMeters)] = struct{}{}
	}
	nPoints := len(used)
	nDistances := len(distinct)

	dm0, _, ok := demographicLevelPrior(in.Demographics)

	// Nothing to fit and no demographic anchor: we cannot honestly predict.
	if nPoints == 0 && !ok {
		return EstimateResult{
			Basis:      "insufficient_data",
			Confidence: "low",
			Version:    EstimatorVersion,
		}
	}

	// Build the diagonal prior N(m0, S0). The slope prior is always the tight
	// Riegel anchor. The level prior depends on what we can lean on:
	//   - demographic standard available → use it, moderate variance.
	//   - else, but we have efforts → seed the level so the prior line passes
	//     through the fastest effort with slope priorBeta1Mean, and make the
	//     level variance diffuse so the data, not the seed, sets the level.
	var mBeta0, s0Beta0 float64
	if ok {
		mBeta0 = dm0
		s0Beta0 = priorBeta0Var
	} else {
		s0Beta0 = diffuseBeta0Var
		// Seed from the single fastest effort: choose β0 so the prior mean
		// line y = β0 + priorBeta1Mean·x passes through (x_fast, y_fast).
		xFast, yFast := fastestEffort(used)
		mBeta0 = yFast - priorBeta1Mean*xFast
	}

	// Prior precision S0inv = diag(1/s0_beta0, 1/priorBeta1Var).
	s0invBeta0 := 1.0 / s0Beta0
	s0invBeta1 := 1.0 / priorBeta1Var

	// Accumulate the normal equations. A is the 2x2 symmetric posterior
	// precision (start from the prior precision), b the 2-vector. phi = [1, x].
	a00 := s0invBeta0
	a01 := 0.0
	a11 := s0invBeta1
	b0 := s0invBeta0 * mBeta0
	b1 := s0invBeta1 * priorBeta1Mean
	for _, u := range used {
		p := u.weight / obsVar // precision contribution of this point
		// phi phiᵀ = [[1, x], [x, x²]] scaled by p.
		a00 += p
		a01 += p * u.x
		a11 += p * u.x * u.x
		// phi·y = [y, x·y] scaled by p.
		b0 += p * u.y
		b1 += p * u.x * u.y
	}

	// Posterior covariance S_N = inverse(A), inverting the 2x2 by hand.
	det := a00*a11 - a01*a01
	s00 := a11 / det
	s01 := -a01 / det
	s11 := a00 / det

	// m_N = S_N · b.
	mN0 := s00*b0 + s01*b1
	mN1 := s01*b0 + s11*b1

	// Predict at x* = ln(target). phi* = [1, x*].
	xStar := math.Log(in.TargetDistanceMeters)
	yhat := mN0 + mN1*xStar
	// v_param = phi*ᵀ S_N phi* = s00 + 2·s01·x* + s11·x*².
	vParam := s00 + 2*s01*xStar + s11*xStar*xStar
	v := vParam + obsVar
	sd := math.Sqrt(v)

	seconds := math.Exp(yhat)
	lower := math.Exp(yhat - bandZ*sd)
	upper := math.Exp(yhat + bandZ*sd)

	res := EstimateResult{
		Seconds:      seconds,
		LowerSeconds: lower,
		UpperSeconds: upper,
		Basis:        basisFor(nPoints, nDistances, ok),
		NPoints:      nPoints,
		NDistances:   nDistances,
		Version:      EstimatorVersion,
	}
	res.Confidence = confidenceFor(seconds, lower, upper)
	return res
}

// fastestEffort returns the (x, y) log coordinates of the effort with the
// best (smallest) pace — i.e. the lowest implied time-at-target under the
// prior slope. We pick the lowest y − priorBeta1Mean·x, which is exactly the
// β0 each effort would imply on its own; the smallest is the fastest athlete.
func fastestEffort(used []usableAttempt) (x, y float64) {
	bestLevel := math.Inf(1)
	for _, u := range used {
		level := u.y - priorBeta1Mean*u.x
		if level < bestLevel {
			bestLevel = level
			x, y = u.x, u.y
		}
	}
	return x, y
}

// basisFor reports which evidence regime produced the estimate, in priority
// order. nPoints==0 with no demographics is handled earlier (insufficient).
func basisFor(nPoints, nDistances int, demographicsOK bool) string {
	if nPoints == 0 && demographicsOK {
		return "demographic_prior"
	}
	if nDistances == 1 {
		return "single_effort"
	}
	return "fitted_curve"
}

// confidenceFor maps the relative half-width of the band to a coarse label.
// h = (upper − lower) / (2·seconds): tighter bands earn more confidence.
func confidenceFor(seconds, lower, upper float64) string {
	if seconds <= 0 {
		return "low"
	}
	h := (upper - lower) / (2 * seconds)
	switch {
	case h <= 0.04:
		return "high"
	case h <= 0.10:
		return "medium"
	default:
		return "low"
	}
}

// distanceBucket keys the distinct-distance count. Rounding to the nearest
// meter collapses floating-point noise (e.g. two "5k" efforts both at 5000.0)
// while keeping genuinely different distances apart.
func distanceBucket(meters float64) string {
	return formatMeters(math.Round(meters))
}

// formatMeters renders a whole-meter value as a compact decimal key without
// pulling in fmt — keeping this file's import set to math only.
func formatMeters(m float64) string {
	if m < 0 {
		m = 0
	}
	n := int64(m)
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
