package dashboard

// TileID is the closed set of dashboard tile identifiers. Ids are the existing
// summary section keys (lifting, not strength_training) so no field renaming is
// needed. The Go catalog is the source of truth for the set — there is no SQL
// CHECK on stored layouts (see migration 049); the write path validates and the
// read path filters, mirroring migration 042's treatment of activity_type.
type TileID string

const (
	TileRunning    TileID = "running"
	TileWalking    TileID = "walking"
	TileCycling    TileID = "cycling"
	TileHiking     TileID = "hiking"
	TileLifting    TileID = "lifting"
	TileSteps      TileID = "steps"
	TileNutrition  TileID = "nutrition"
	TileBodyweight TileID = "bodyweight"
	TileRecovery   TileID = "recovery"
	TileStreak     TileID = "streak"
)

// Catalog is the ordered set of every tile. Order fixes how tiles appear in the
// web add-tile tray. The contract test (tiles_test.go) and the TS mirror
// (lib/dashboard-tiles.ts) assert this list stays identical across the boundary.
var Catalog = []TileID{
	TileRunning, TileWalking, TileCycling, TileHiking, TileLifting,
	TileSteps, TileNutrition, TileBodyweight, TileRecovery, TileStreak,
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
