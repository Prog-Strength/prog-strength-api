package server

import (
	"context"
	"errors"
	"log"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarsync"
)

// reconcileActivityCalendar writes the Google Calendar events that the inline
// sync hooks owed but never landed. It runs on every boot, not once.
//
// This exists for the same reason reconcileTimeline does, and the two should
// be read together. Publishing to the feed is best-effort, and that was only
// SAFE once a pass repaired the gaps — before it existed, a single dropped
// write stayed dropped until a bug report surfaced it months later. Calendar
// writes are best-effort in exactly the same way, except they also depend on
// a third party being reachable, so the failure rate is structurally higher.
// A best-effort integration with no repair pass is not eventually consistent;
// it is just lossy.
//
// Three things make this different from the timeline reconciler, all of them
// consequences of writing to somebody else's API rather than our own SQLite:
//
//   - It is BOUNDED (cfg reconcile_max_per_boot). Each repair is a real
//     Google API call, so an unbounded pass after a long outage could trip
//     rate limits and take down the integration it is trying to repair.
//     Truncation is logged, never silent — a pass that quietly did 200 of
//     4,000 and reported success would be a lie of exactly the kind this
//     feature is meant to eliminate.
//   - It runs in the BACKGROUND. The timeline pass is local and fast enough
//     to block boot; hundreds of round trips to Google are not, and a slow
//     Google must never delay the API becoming healthy.
//   - It is SCOPED BY POLICY, not just by absence. PendingSince applies the
//     no-backfill cutoff (activities logged after the user connected) and the
//     retry cap, so this can never resurrect a user's entire history.
func reconcileActivityCalendar(
	ctx context.Context,
	svc *calendarsync.ActivityService,
	state calendarsync.ActivitySyncRepository,
	maxAttempts, limit int,
) {
	pending, err := state.PendingSince(ctx, maxAttempts, limit)
	if err != nil {
		log.Printf("calendar reconcile: list pending failed: %v", err)
		calendarsync.ObserveReconcileRun("error", 0)
		return
	}
	if len(pending) == 0 {
		calendarsync.ObserveReconcileRun("ok", 0)
		return
	}

	var synced, skipped, failed int
	for _, p := range pending {
		// Honor cancellation: a shutdown mid-pass should stop promptly, and
		// whatever is left is simply picked up by the next boot.
		if ctx.Err() != nil {
			log.Printf("calendar reconcile: cancelled after %d writes, %d left this pass",
				synced+failed+skipped, len(pending)-(synced+failed+skipped))
			calendarsync.ObserveReconcileRun("cancelled", len(pending)-(synced+failed+skipped))
			return
		}
		switch err := svc.ReconcileActivity(ctx, p.UserID, p.ActivityID); {
		case err == nil:
			synced++
		case errors.Is(err, calendarsync.ErrSyncSkipped):
			skipped++
		default:
			failed++
			log.Printf("calendar reconcile: activity %s: %v", p.ActivityID, err)
		}
	}

	// Report the backlog this pass could not reach. A full page means there
	// are probably more behind it, which is the signal that the cap is too
	// low for the current write rate.
	backlog := 0
	if len(pending) == limit {
		backlog = limit
		log.Printf("calendar reconcile: hit the per-boot cap of %d; more activities remain and will be retried next boot", limit)
	}
	log.Printf("calendar reconcile: synced=%d skipped=%d failed=%d of %d pending", synced, skipped, failed, len(pending))
	calendarsync.ObserveReconcileRun("ok", backlog)
}
