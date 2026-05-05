package exercise

// Equipment identifies a piece of equipment required to perform an exercise.
// EquipmentNone represents bodyweight exercises.
//
// Bench angles are modeled as distinct equipment values because the angle
// meaningfully changes which muscles are emphasized — e.g., incline bench
// press shifts load to the upper chest and front delts, while decline
// shifts to the lower chest. Treating them as separate equipment keeps
// the catalog explicit and avoids ambiguous logging.
type Equipment string

const (
	EquipmentNone           Equipment = "none"
	EquipmentBarbell        Equipment = "barbell"
	EquipmentDumbbell       Equipment = "dumbbell"
	EquipmentKettlebell     Equipment = "kettlebell"
	EquipmentCable          Equipment = "cable"
	EquipmentMachine        Equipment = "machine"
	EquipmentResistanceBand Equipment = "resistance_band"
	EquipmentPullupBar      Equipment = "pullup_bar"
	EquipmentFlatBench      Equipment = "flat_bench"
	EquipmentInclineBench   Equipment = "incline_bench"
	EquipmentDeclineBench   Equipment = "decline_bench"
	EquipmentRack           Equipment = "rack"
)

// Valid reports whether e is a known Equipment.
func (e Equipment) Valid() bool {
	switch e {
	case EquipmentNone, EquipmentBarbell, EquipmentDumbbell, EquipmentKettlebell,
		EquipmentCable, EquipmentMachine, EquipmentResistanceBand, EquipmentPullupBar,
		EquipmentFlatBench, EquipmentInclineBench, EquipmentDeclineBench, EquipmentRack:
		return true
	}
	return false
}
