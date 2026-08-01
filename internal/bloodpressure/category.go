// Package bloodpressure tracks blood-pressure readings keyed by user +
// timestamp. See prog-strength-docs/sows/blood-pressure.md.
//
// Unlike bodyweight there is no per-user goal: the healthy range is a
// published clinical classification, identical for every user (see
// Classify below), not per-user state.
package bloodpressure

// Category is the ACC/AHA blood-pressure classification a reading falls
// into. Values are the lowercase snake_case wire form so handlers can
// emit them directly without a mapping table.
type Category string

const (
	CategoryNormal   Category = "normal"
	CategoryElevated Category = "elevated"
	CategoryStage1   Category = "stage_1"
	CategoryStage2   Category = "stage_2"
	CategoryCrisis   Category = "crisis"
)

// Classify maps a systolic/diastolic pair to its clinical category.
//
// Higher category wins: a reading lands in the most severe band either
// number reaches, so the switch checks bands from most to least severe
// and returns on the first match. The case order is therefore
// load-bearing — reordering would misclassify readings whose two numbers
// straddle a boundary (e.g. 135/75 is stage_1 by systolic even though its
// diastolic alone reads normal).
func Classify(systolic, diastolic int) Category {
	switch {
	case systolic >= 180 || diastolic >= 120:
		return CategoryCrisis
	case systolic >= 140 || diastolic >= 90:
		return CategoryStage2
	case systolic >= 130 || diastolic >= 80:
		return CategoryStage1
	case systolic >= 120:
		return CategoryElevated
	default:
		return CategoryNormal
	}
}
