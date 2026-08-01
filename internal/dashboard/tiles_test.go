package dashboard

import "testing"

func TestCatalog_EveryConstantAppearsExactlyOnce(t *testing.T) {
	all := []TileID{
		TileRunning, TileWalking, TileCycling, TileHiking, TileLifting,
		TileSteps, TileNutrition, TileBodyweight, TileBloodPressure, TileRecovery, TileStreak,
	}
	if len(Catalog) != len(all) {
		t.Fatalf("Catalog has %d entries, expected %d", len(Catalog), len(all))
	}
	seen := map[TileID]int{}
	for _, id := range Catalog {
		seen[id]++
	}
	for _, id := range all {
		if seen[id] != 1 {
			t.Errorf("tile %q appears %d times in Catalog, want exactly 1", id, seen[id])
		}
	}
}

func TestCatalog_Order(t *testing.T) {
	want := []TileID{
		TileRunning, TileWalking, TileCycling, TileHiking, TileLifting,
		TileSteps, TileNutrition, TileBodyweight, TileBloodPressure, TileRecovery, TileStreak,
	}
	for i := range want {
		if Catalog[i] != want[i] {
			t.Errorf("Catalog[%d] = %q, want %q", i, Catalog[i], want[i])
		}
	}
}

func TestValidTileID(t *testing.T) {
	if !ValidTileID("running") {
		t.Error("running should be valid")
	}
	if ValidTileID("strength_training") {
		t.Error("strength_training is not a tile id")
	}
	if ValidTileID("") {
		t.Error("empty is not valid")
	}
}
