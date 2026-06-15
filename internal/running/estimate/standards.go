package estimate

import "math"

// standards.go is the demographic anchor for the prior level (β0). When the
// athlete has no effort history we have nothing to fit, so the prediction
// falls back entirely on a population standard time for their age and sex.
//
// DEFERRED / ITERATION DATA: the real product owns a calibrated standards
// table keyed on age + sex (and eventually height/weight). That table is not
// wired up yet — age and sex are not even persisted today (only height is
// available). So v1 ships a deliberately minimal PLACEHOLDER table whose only
// job is to make the demographic_prior code path real and testable. Do not
// read clinical meaning into these numbers; replace them when the standards
// data lands (see README "How to iterate" → add a demographic factor).

// refDistanceMeters is the distance the placeholder standard is quoted at.
// β0 is recovered by projecting this reference time back to ln(distance)=0
// along the prior's Riegel slope, so the whole prior line is pinned by one
// (distance, time) anchor.
const refDistanceMeters = 5000.0 // D_ref: the standard is a 5K time

// baseMale5kSeconds is the placeholder reference: a moderately-trained adult
// male 5K time at the reference age. The female and age adjustments scale off
// this single number. Minimal but real enough to exercise the prior path.
const baseMale5kSeconds = 1500.0 // 25:00 over 5K
const refAgeYears = 30.0         // age the base time is quoted at
const femaleFactor = 1.10        // placeholder: female standard ~10% slower
const agePerYearFactor = 0.005   // placeholder: ~0.5% slower per year past ref
const heightRefCm = 175.0        // placeholder reference stature
const heightPerCmFactor = 0.001  // placeholder: tiny stature refinement

// demographicLevelPrior returns the prior mean for β0 (the log-time level)
// implied by the athlete's demographics, the variance to use for it, and
// whether a standard could be derived at all.
//
// v1 contract: ok is false UNLESS both Age and Sex are present, because the
// placeholder table is keyed on age+sex. With both present we look up a
// standard time T_std at D_ref, then convert to a β0 prior using the model's
// own slope (priorBeta1Mean): since y = β0 + β1·x and the standard gives the
// point (x_ref, ln T_std), β0 = ln(T_std) − β1·x_ref. Height, when present,
// applies a small documented multiplicative refinement to T_std.
func demographicLevelPrior(d Demographics) (mBeta0 float64, variance float64, ok bool) {
	if d.Age == nil || d.Sex == nil {
		return 0, 0, false
	}

	tStd := baseMale5kSeconds
	if *d.Sex == "female" {
		tStd *= femaleFactor
	}
	// Age adjustment: slower the further past the reference age (and a touch
	// faster below it). Symmetric and linear — a placeholder, not a curve.
	tStd *= 1 + agePerYearFactor*(float64(*d.Age)-refAgeYears)

	// Optional height refinement: taller athletes get a small downward nudge
	// on the standard time. Documented as cosmetic until real data exists.
	if d.HeightCm != nil {
		tStd *= 1 - heightPerCmFactor*(*d.HeightCm-heightRefCm)
	}

	if tStd <= 0 {
		// Defensive: an absurd age/height combo could drive the factor
		// non-positive; refuse to emit a nonsensical log.
		return 0, 0, false
	}

	xRef := math.Log(refDistanceMeters)
	mBeta0 = math.Log(tStd) - priorBeta1Mean*xRef
	return mBeta0, priorBeta0Var, true
}
