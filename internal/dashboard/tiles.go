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
	// TileRecoveryTrend is RETIRED: the web tile it named was folded into
	// hrv_balance, which is now a two-view card (balance, then the trend rail).
	// It is deliberately absent from Catalog — no client may add it — and kept
	// as a constant only so RetiredTiles can name it. Layouts that still hold it
	// are migrated, not truncated; see RetiredTiles.
	TileRecoveryTrend TileID = "recovery_trend"
	TileRecoveryLog   TileID = "recovery_log"
	TileStreak        TileID = "streak"
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
	TileRecovery, TileHRVBalance, TileMorningVitals, TileRecoveryLog,
	TileStreak, TileQuote, TileWeather,
}

// RetiredTiles maps a tile that no longer exists to the tile that absorbed it.
//
// A retired id is NOT merely unknown. Normalize drops unknown ids, which is
// right for a typo or a tile that was deleted outright, but wrong for a tile
// that was merged into another one: the user placed something on their
// dashboard and it still exists, under a different id. Resolving instead of
// dropping keeps it in the slot they put it in.
//
// The write path resolves too, so a browser tab left open across the deploy
// saves successfully instead of 422-ing on an id it was served last week.
var RetiredTiles = map[TileID]TileID{
	TileRecoveryTrend: TileHRVBalance,
}

// ResolveTileID maps a retired id to its replacement and returns every other id
// unchanged (including unknown ones, which remain the caller's to reject).
func ResolveTileID(id TileID) TileID {
	if replacement, ok := RetiredTiles[id]; ok {
		return replacement
	}
	return id
}

// resolveSectionTiles applies ResolveTileID across a layout, dropping a tile
// that RESOLVING has turned into a duplicate of one already kept — a user who
// had both hrv_balance and recovery_trend on their dashboard ends up with one
// hrv_balance, in the earlier of the two slots.
//
// Only a collision that a retirement caused is absorbed, in either direction
// (the retired id may come first or second). A client sending the same LIVE id
// twice is untouched, so the write path still reports it as the client error it
// is rather than having it quietly repaired here.
func resolveSectionTiles(sections []Section) []Section {
	out := make([]Section, len(sections))
	seen := make(map[TileID]bool)
	fromRetired := make(map[TileID]bool)
	for i, s := range sections {
		tiles := make([]TileID, 0, len(s.TileIDs))
		for _, id := range s.TileIDs {
			resolved := ResolveTileID(id)
			retired := resolved != id
			if seen[resolved] && (retired || fromRetired[resolved]) {
				continue
			}
			seen[resolved] = true
			if retired {
				fromRetired[resolved] = true
			}
			tiles = append(tiles, resolved)
		}
		out[i] = s
		out[i].TileIDs = tiles
	}
	return out
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
