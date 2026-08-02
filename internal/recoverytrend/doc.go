// Package recoverytrend is the single source of truth for the dashboard's
// recovery-trend math: per-metric trailing baselines, the per-user HRV balance
// band, and the near-term HRV trend. Like internal/hrzones it is deliberately
// pure — it imports nothing from the database, HTTP, config, or clock layers,
// and defines its own value types (Day, Baseline, HRV) rather than the wire
// DTOs — so the same math is shared by every caller and unit-tested in
// isolation. Callers own data access and map their rows into a []Day; the
// engine owns the numbers.
//
// # The window
//
// Compute takes a date-ascending window whose FINAL element is today's local
// date. Every day is present even when Whoop has no row for it: absent metrics
// are nil and are skipped, never treated as zero. The baseline sample is every
// day EXCEPT the last — today is excluded so a day is measured against its
// history rather than against a window it is a member of. At the default 30-day
// window an included day would shift its own baseline by a few percent of its
// own deviation; small, but "today vs your baseline" should mean what it says.
//
// # Averages and spread
//
// Each metric's average is the arithmetic mean over its non-null values in the
// baseline sample, emitted only once at least MinBaselineDays of them exist;
// below that the average is nil but the real sample count still ships, so a
// client can render an honest "calibrating, 9 of 14 days" instead of a
// confident number backed by nine readings. Counts are per metric, not per day.
// HRV additionally carries the population standard deviation (divide by n): the
// sample is the athlete's actual recorded history, and at n≈30 the population
// and sample SDs differ by under 2%.
//
// # The balanced band
//
// The divisor is the FLOORED standard deviation, max(sd, MinStdDevMs), so a
// near-flat history cannot produce an unbounded z-score. Both the band bounds
// and the z-score use that same floored SD, so the emitted bounds and the
// emitted status can never disagree:
//
//	balanced_low  = hrv_avg − balanced_z × sd_effective
//	balanced_high = hrv_avg + balanced_z × sd_effective
//	z_score       = (hrv_today − hrv_avg) / sd_effective
//
// Status is directional and inclusive at the boundary: |z| ≤ balanced_z reads
// balanced, z > balanced_z elevated, z < −balanced_z suppressed. Elevated and
// suppressed are kept distinct because HRV above baseline is a different signal
// from HRV below it; a two-state client can merge them at render time, but the
// reverse is impossible. Status is unknown when there is no HRV baseline or
// today has no reading — an absent morning webhook must not read as stale
// readiness by promoting yesterday.
//
// # The trend
//
// The trend compares the mean HRV over the last TrendWindowDays entries
// (INCLUDING today) against the same baseline, reported rising/falling/steady
// once the window holds at least MinTrendDays samples and unknown otherwise.
// The windows overlap by design — the recent days sit inside the baseline — so
// the product tells one story ("X below your 30-day baseline") with one
// baseline. Overlap dampens the measured delta but cannot invert it, and
// TrendZ is calibrated against that dampened scale.
package recoverytrend
