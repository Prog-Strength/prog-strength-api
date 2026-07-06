package estimate

import "math"

// standards.go anchors the prior level (β0) from age/sex population norms.
// Values are weak priors derived from typical age-graded 5K equivalences
// (WMA-style factors, rounded for a small embedded table). They are not
// clinical predictions — the user's own efforts dominate once present.

const refDistanceMeters = 5000.0

// baseMale5kByBand maps the lower bound of a 5-year age band to a reference
// male 5K time in seconds at moderate fitness. Bands below 20 or above 70
// clamp to the nearest edge.
var baseMale5kByBand = map[int]float64{
	20: 1380, // 23:00
	25: 1410,
	30: 1440, // 24:00
	35: 1470,
	40: 1500, // 25:00
	45: 1530,
	50: 1590,
	55: 1650,
	60: 1710,
	65: 1770,
	70: 1830,
}

const femaleFactor = 1.10
const heightRefCm = 175.0
const heightPerCmFactor = 0.001

func demographicLevelPrior(d Demographics) (mBeta0 float64, variance float64, ok bool) {
	if d.Sex == nil {
		return 0, 0, false
	}

	tStd := reference5kSeconds(d)
	if tStd <= 0 {
		return 0, 0, false
	}

	if d.HeightCm != nil {
		tStd *= 1 - heightPerCmFactor*(*d.HeightCm-heightRefCm)
	}
	if tStd <= 0 {
		return 0, 0, false
	}

	xRef := math.Log(refDistanceMeters)
	mBeta0 = math.Log(tStd) - priorBeta1Mean*xRef
	return mBeta0, priorBeta0Var, true
}

func reference5kSeconds(d Demographics) float64 {
	age := 30
	if d.Age != nil {
		age = *d.Age
	}
	tStd := male5kForAge(age)
	if *d.Sex == "female" {
		tStd *= femaleFactor
	}
	return tStd
}

func male5kForAge(age int) float64 {
	if age < 20 {
		age = 20
	}
	if age > 70 {
		age = 70
	}
	band := (age / 5) * 5
	if age%5 != 0 && age < 70 {
		// Linear interpolate between band and next band when between anchors.
		next := band + 5
		if next > 70 {
			next = 70
		}
		lo := baseMale5kByBand[band]
		hi := baseMale5kByBand[next]
		frac := float64(age-band) / float64(next-band)
		return lo + frac*(hi-lo)
	}
	return baseMale5kByBand[band]
}
