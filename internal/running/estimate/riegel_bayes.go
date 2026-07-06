package estimate

import "math"

// riegel_bayes.go is the v2 model: weighted Bayesian linear regression in
// log-space plus a logged-best floor post-process.

const (
	priorBeta1Mean     = 1.06
	priorBeta1Var      = 0.0025
	priorBeta0Var      = 0.25
	diffuseBeta0Var    = 100.0
	tauDays            = 180.0
	obsVar             = 0.0009
	bandZ              = 1.0
	qualityFloor       = 0.25
	qualityKnee        = 0.9
	paceRatioFloor     = 0.25
	paceRatioKnee      = 1.15
	hrIntensityFloor   = 0.25
	hrIntensityFullPct = 0.25
	anchorBoost        = 2.0
)

type riegelBayes struct{}

type usableAttempt struct {
	x      float64
	y      float64
	weight float64
}

func (riegelBayes) Estimate(in EstimateInput) EstimateResult {
	used := make([]usableAttempt, 0, len(in.Attempts))
	distinct := map[string]struct{}{}
	for _, a := range in.Attempts {
		if a.DurationSeconds <= 0 || a.DistanceMeters <= 0 {
			continue
		}
		w := attemptWeight(in.Now, a)
		if w <= 0 {
			continue
		}
		used = append(used, usableAttempt{
			x:      math.Log(a.DistanceMeters),
			y:      math.Log(a.DurationSeconds),
			weight: w,
		})
		distinct[distanceBucket(a.DistanceMeters)] = struct{}{}
	}
	nPoints := len(used)
	nDistances := len(distinct)

	dm0, _, ok := demographicLevelPrior(in.Demographics)

	if nPoints == 0 && !ok {
		return EstimateResult{
			Basis:      "insufficient_data",
			Confidence: "low",
			Version:    EstimatorVersion,
		}
	}

	var mBeta0, s0Beta0 float64
	if ok {
		mBeta0 = dm0
		s0Beta0 = priorBeta0Var
	} else {
		s0Beta0 = diffuseBeta0Var
		xFast, yFast := fastestEffort(used)
		mBeta0 = yFast - priorBeta1Mean*xFast
	}

	s0invBeta0 := 1.0 / s0Beta0
	s0invBeta1 := 1.0 / priorBeta1Var

	a00 := s0invBeta0
	a01 := 0.0
	a11 := s0invBeta1
	b0 := s0invBeta0 * mBeta0
	b1 := s0invBeta1 * priorBeta1Mean
	for _, u := range used {
		p := u.weight / obsVar
		a00 += p
		a01 += p * u.x
		a11 += p * u.x * u.x
		b0 += p * u.y
		b1 += p * u.x * u.y
	}

	det := a00*a11 - a01*a01
	s00 := a11 / det
	s01 := -a01 / det
	s11 := a00 / det

	mN0 := s00*b0 + s01*b1
	mN1 := s01*b0 + s11*b1

	xStar := math.Log(in.TargetDistanceMeters)
	yhat := mN0 + mN1*xStar
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
		RawSeconds:   seconds,
		Basis:        basisFor(nPoints, nDistances, ok),
		NPoints:      nPoints,
		NDistances:   nDistances,
		Version:      EstimatorVersion,
	}
	res.Confidence = confidenceFor(seconds, lower, upper)
	return applyLoggedBestFloor(res, in.LoggedBestSeconds)
}

func applyLoggedBestFloor(res EstimateResult, loggedBest *float64) EstimateResult {
	if loggedBest == nil || *loggedBest <= 0 || res.Basis == "insufficient_data" {
		return res
	}
	if res.RawSeconds == 0 {
		res.RawSeconds = res.Seconds
	}
	if res.Seconds > *loggedBest {
		res.Seconds = *loggedBest
		res.FlooredAtLoggedBest = true
	}
	if res.LowerSeconds > *loggedBest {
		res.LowerSeconds = *loggedBest
	}
	if res.FlooredAtLoggedBest {
		res.Basis = "logged_best_floor"
	}
	return res
}

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

func basisFor(nPoints, nDistances int, demographicsOK bool) string {
	if nPoints == 0 && demographicsOK {
		return "demographic_prior"
	}
	if nDistances == 1 {
		return "single_effort"
	}
	return "fitted_curve"
}

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

func distanceBucket(meters float64) string {
	return formatMeters(math.Round(meters))
}

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
