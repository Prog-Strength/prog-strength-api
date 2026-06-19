package dashboard

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// headlineOneRM picks the user's flagship estimated one-rep max for the lifting
// tile: across every exercise the user has a PR on, the one whose current
// recency-weighted baseline is highest. The baseline math mirrors the
// /personal-records handler exactly (same window/tau, same most-recent-in-window
// unit convention) so the tile and that page agree.
//
// Returns nil when no exercise has a computable in-window baseline (e.g. all the
// user's PRs are older than DefaultBaselineWindow), which the builder renders as
// a null HeadlineEstimated1RM. A history read error for one exercise skips that
// exercise rather than failing the whole tile — the headline is best-effort.
func headlineOneRM(
	ctx context.Context,
	repo workout.Repository,
	exerciseRepo exercise.Repository,
	userID string,
	prs []workout.PersonalRecord,
	now time.Time,
) *Headline1RM {
	since := now.Add(-workout.DefaultBaselineWindow)
	until := now

	var best *Headline1RM
	for _, pr := range prs {
		entries, err := repo.ListOneRepMaxHistory(ctx, userID, pr.ExerciseID, &since, &until)
		if err != nil {
			// Best-effort: a single exercise's history read failing shouldn't
			// drop the whole headline; just skip it.
			continue
		}
		baseline, ok := workout.RecencyWeightedBaseline(entries, now, workout.DefaultBaselineWindow, workout.DefaultBaselineTau)
		if !ok {
			continue
		}
		if best != nil && baseline <= best.Value {
			continue
		}

		// Unit mirrors the most-recent in-window entry's unit, the same
		// convention the personal-records and progression handlers use.
		unit := ""
		for _, e := range entries {
			if !e.PerformedAt.Before(since) {
				unit = string(e.Unit)
				break
			}
		}

		best = &Headline1RM{
			ExerciseName: resolveExerciseName(ctx, exerciseRepo, pr.ExerciseID),
			Value:        baseline,
			Unit:         unit,
		}
	}
	return best
}

// resolveExerciseName resolves a display name from the exercise catalog,
// falling back to the slug on any error — mirroring the workout handler's
// personalRecords so a catalog mismatch still renders something.
func resolveExerciseName(ctx context.Context, exerciseRepo exercise.Repository, exerciseID string) string {
	if ex, err := exerciseRepo.GetByID(ctx, exerciseID); err == nil {
		return ex.Name
	}
	return exerciseID
}
