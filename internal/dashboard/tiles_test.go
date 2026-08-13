package dashboard

import "testing"

func TestCatalog_EveryConstantAppearsExactlyOnce(t *testing.T) {
	all := []TileID{
		TileRunning, TileRunningLog, TileRunningEffort, TileRunningVertical,
		TileWalking, TileCycling, TileHiking, TileLifting,
		TileSteps, TileNutrition, TileBodyweight, TileBloodPressure,
		TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryLog, TileRestingHR,
		TileSleep, TileStreak, TileQuote, TileWeather, TileCalendar,
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
		TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryLog, TileRestingHR,
		TileSleep, TileStreak, TileQuote, TileWeather, TileCalendar,
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
	if !ValidTileID("resting_hr") {
		t.Error("resting_hr should be valid")
	}
	if !ValidTileID("sleep") {
		t.Error("sleep should be valid")
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
	if !ValidTileID("calendar") {
		t.Error("calendar should be valid")
	}
	// Retired: folded into hrv_balance, so no client may place it any more.
	if ValidTileID("recovery_trend") {
		t.Error("recovery_trend is retired and must not validate")
	}
}

func TestResolveTileID_RetiredTileBecomesItsReplacement(t *testing.T) {
	if got := ResolveTileID(TileRecoveryTrend); got != TileHRVBalance {
		t.Errorf("ResolveTileID(recovery_trend) = %q, want hrv_balance", got)
	}
	if got := ResolveTileID(TileSteps); got != TileSteps {
		t.Errorf("ResolveTileID(steps) = %q, want steps unchanged", got)
	}
	// An unknown id is not this function's problem — callers still reject it.
	if got := ResolveTileID("no_such_tile"); got != "no_such_tile" {
		t.Errorf("ResolveTileID(no_such_tile) = %q, want it unchanged", got)
	}
	// Every replacement must itself be a live tile, or a layout would be
	// migrated onto an id that Normalize then drops.
	for retired, replacement := range RetiredTiles {
		if ValidTileID(retired) {
			t.Errorf("retired tile %q is still in the catalog", retired)
		}
		if !ValidTileID(replacement) {
			t.Errorf("tile %q is retired into %q, which is not in the catalog", retired, replacement)
		}
	}
}
