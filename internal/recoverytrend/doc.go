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
//
// # The series and the drift
//
// Compute answers one question about one day. ComputeSeries answers it once per
// charted day: for every day in the series it derives that day's OWN trailing
// baseline from the BaselineWindowDays that preceded it, applying the same
// exclude-the-day-itself rule Compute applies to today. A client can therefore
// color each night against the band as it stood on that night's morning rather
// than against today's band, which would measure July against an August
// baseline.
//
// This is why the input is WIDE. ComputeSeries takes BaselineWindowDays of
// lead-in ahead of the first charted day — 61 dates for a 31-day series at the
// default window — so the OLDEST charted day still has a full trailing sample.
// Without the lead-in the left edge of the band would wobble as the sample
// truncated, an artifact of the window rather than anything about the athlete.
// The lead-in is input only; the returned slice covers days[BaselineWindowDays:]
// and nothing older is ever serialized. An input no longer than the lead-in has
// no charted days at all and returns an empty series.
//
// Both entry points share band and classify, so the bounds and status the
// series reports for its last day are bit-identical to the scalar HRV block
// computed from the same sample. That agreement is structural, not a
// convention, and it is pinned by test.
//
// BaselineTrend is a DIFFERENT question from HRV.Trend, and the distinction is
// the point of the whole series. HRV.Trend compares the recent TrendWindowDays
// mean against the baseline it sits inside: "is this week off my normal?"
// BaselineTrend compares the baseline against the baseline as it stood
// BaselineDriftDays ago: "is my normal itself moving?" A climbing baseline is
// adaptation; a sinking one is a reason to look at sleep, load, or illness. The
// two can legitimately point opposite ways — a rising baseline under a
// suppressed morning is a real state, not a bug, and a client that renders both
// must not try to reconcile them.
//
// The drift threshold is SD-relative, like every other threshold here: a 6 ms
// move is signal for an athlete whose spread is 8 ms and noise for one whose
// spread is 25 ms. It uses the MOST RECENT day's effective SD, so the verdict is
// scaled to the spread the athlete has now rather than one they have grown out
// of. Direction is unknown when the series cannot reach back BaselineDriftDays,
// when either endpoint has no baseline yet, or when no charted day ever reached
// MinBaselineDays — the same honest silence the bounds already use.
package recoverytrend
