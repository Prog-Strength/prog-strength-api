package dashboard

import "testing"

func TestCatalog_EveryConstantAppearsExactlyOnce(t *testing.T) {
	all := []TileID{
		TileRunning, TileRunningLog, TileRunningEffort, TileRunningVertical,
		TileWalking, TileCycling, TileHiking, TileLifting,
		TileSteps, TileNutrition, TileBodyweight, TileBloodPressure,
		TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryTrend, TileRecoveryLog,
		TileStreak, TileQuote, TileWeather,
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
		TileRunning, TileRunningLog, TileRunningEffort, TileRunningVertical,
		TileWalking, TileCycling, TileHiking, TileLifting,
		TileSteps, TileNutrition, TileBodyweight, TileBloodPressure,
		TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryTrend, TileRecoveryLog,
		TileStreak, TileQuote, TileWeather,
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
	if !ValidTileID("hrv_balance") {
		t.Error("hrv_balance should be valid")
	}
	if !ValidTileID("recovery_log") {
		t.Error("recovery_log should be valid")
	}
	if !ValidTileID("running_log") {
		t.Error("running_log should be valid")
	}
	if !ValidTileID("running_effort") {
		t.Error("running_effort should be valid")
	}
	if !ValidTileID("running_vertical") {
		t.Error("running_vertical should be valid")
	}
	if !ValidTileID("quote") {
		t.Error("quote should be valid")
	}
	if !ValidTileID("weather") {
		t.Error("weather should be valid")
	}
}
