package dashboard

// TileID is the closed set of dashboard tile identifiers. Ids are the existing
// summary section keys (lifting, not strength_training) so no field renaming is
// needed. The Go catalog is the source of truth for the set — there is no SQL
// CHECK on stored layouts (see migration 049); the write path validates and the
// read path filters, mirroring migration 042's treatment of activity_type.
type TileID string

const (
	TileRunning         TileID = "running"
	TileRunningLog      TileID = "running_log"
	TileRunningEffort   TileID = "running_effort"
	TileRunningVertical TileID = "running_vertical"
	TileWalking         TileID = "walking"
	TileCycling         TileID = "cycling"
	TileHiking          TileID = "hiking"
	TileLifting         TileID = "lifting"
	TileSteps           TileID = "steps"
	TileNutrition       TileID = "nutrition"
	TileBodyweight      TileID = "bodyweight"
	TileBloodPressure   TileID = "blood_pressure"
	TileRecovery        TileID = "recovery"
	TileHRVBalance      TileID = "hrv_balance"
	TileMorningVitals   TileID = "morning_vitals"
	TileRecoveryTrend   TileID = "recovery_trend"
	TileRecoveryLog     TileID = "recovery_log"
	TileStreak          TileID = "streak"
	// TileQuote is the one tile with no user data behind it: its content is a
	// static corpus compiled into the binary (internal/quotes), so it has no
	// repository, no window, and no empty state.
	TileQuote TileID = "quote"
	// TileWeather has NO summary section: the tile self-fetches from
	// GET /weather so a slow OpenWeather day can never slow the dashboard
	// (see the SOW's "Why weather is not a summary section"). The catalog
	// entry exists only so layouts can place and validate the tile.
	TileWeather TileID = "weather"
)

// Catalog is the ordered set of every tile. Order fixes how tiles appear in the
// web add-tile tray. The contract test (tiles_test.go) and the TS mirror
// (lib/dashboard-tiles.ts) assert this list stays identical across the boundary.
var Catalog = []TileID{
	TileRunning, TileRunningLog, TileRunningEffort, TileRunningVertical,
	TileWalking, TileCycling, TileHiking, TileLifting,
	TileSteps, TileNutrition, TileBodyweight, TileBloodPressure,
	TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryTrend, TileRecoveryLog,
	TileStreak, TileQuote, TileWeather,
}

var catalogSet = func() map[TileID]bool {
	m := make(map[TileID]bool, len(Catalog))
	for _, id := range Catalog {
		m[id] = true
	}
	return m
}()

// ValidTileID reports whether id is a known tile.
func ValidTileID(id TileID) bool { return catalogSet[id] }
