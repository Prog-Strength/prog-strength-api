// Package hrzones is the single source of truth for heart-rate-zone logic:
// the zone model, reference-max-HR estimation, classification, and
// time-in-zone accumulation. It is deliberately pure — it imports nothing from
// the database, HTTP, or activity layers — so the same boundary math is shared
// by every caller and can be unit-tested in isolation. Callers own data access
// and assemble a Stats summary; the engine owns the math.
//
// # Zone model
//
// The model is percent-of-max: five zones derived from four ascending interior
// ZoneUpperBounds (e.g. [0.60,0.70,0.80,0.90]). Zone i spans LowerPct..UpperPct
// of the reference max-HR, lower inclusive and upper exclusive. The model is
// open-ended at both ends on purpose: the bottom zone starts at 0 so very low
// HR (and the warm-up ramp) always lands somewhere, and the top zone has no
// ceiling so a maximal effort that briefly exceeds the reference is still
// counted rather than dropped. Classification compares the fractional threshold
// (value < UpperPct*maxHR) directly, and the per-zone bpm bounds shown for
// display are derived from that same threshold (MinBpm = ceil(LowerPct*maxHR),
// MaxBpm = ceil(UpperPct*maxHR)-1) so a value is binned exactly once and the
// displayed range never disagrees with where its time is counted. Intervals are
// classified by the mean of their two endpoint bpms.
//
// # Estimation ladder and confidence
//
// A correct breakdown needs a trustworthy max-HR, which a single run rarely
// gives. EstimateReference climbs a ladder as HR history accumulates:
//
//   - estimated  — cold start. Begin from the population default; if the
//     current run's own p99 already exceeds it, prefer that ("current_run").
//   - calibrating — some history exists. Use the p99 over recent runs, raised
//     to the current run's p99 if higher.
//   - calibrated  — enough qualifying runs exist. Trust the p99 over recent
//     runs outright.
//
// The robust statistic throughout is the 99th percentile (P99, nearest-rank),
// not the raw maximum: HR straps emit occasional spurious spikes, and a lone
// 220 among otherwise 150s must not define the top of a user's zones. The final
// reference is clamped to a plausible band so neither a sensor glitch nor a
// sparse early history can produce an absurd max-HR.
//
// The Confidence on the returned Reference is surfaced to the client as the
// Result.Calibrating flag (true whenever confidence is not calibrated), which
// drives the "still calibrating" treatment in the zones widget so early,
// low-confidence breakdowns are shown honestly rather than as settled truth.
//
// # Growth path
//
// Computing RecentHRSamplesP99 today means scanning recent runs' trackpoints at
// read time. Because the engine boundary keeps all zone logic here, that cost
// can be optimized away without touching the engine: persist each run's robust
// HR statistic (its p99) to a nullable activities column at ingest, and the
// repository's RecentHRStats becomes a cheap column aggregate over that column
// instead of a trackpoint scan. Only the repository changes; this package's API
// and semantics stay fixed.
package hrzones
