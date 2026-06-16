package server

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// liftSessionSource adapts the workout repository to user.LiftSessionSource so
// the user package's profile-stats handler can read completed lift sessions
// without importing workout (which would be a cross-domain import the user
// package is kept free of). The adapter lives in the wiring layer and maps
// workout.SessionDuration → user.LiftSession.
type liftSessionSource struct {
	repo workout.Repository
}

var _ user.LiftSessionSource = (*liftSessionSource)(nil)

func newLiftSessionSource(repo workout.Repository) *liftSessionSource {
	return &liftSessionSource{repo: repo}
}

func (s *liftSessionSource) ListCompletedSessionsSince(ctx context.Context, userID string, since time.Time) ([]user.LiftSession, error) {
	rows, err := s.repo.ListCompletedSessionsSince(ctx, userID, since)
	if err != nil {
		return nil, err
	}
	out := make([]user.LiftSession, len(rows))
	for i, r := range rows {
		out[i] = user.LiftSession{PerformedAt: r.PerformedAt, EndedAt: r.EndedAt}
	}
	return out, nil
}

// runningSampleSource adapts the activity repository to user.RunningSampleSource,
// mapping activity.RunSample → user.RunningSample for the same import-isolation
// reason as liftSessionSource.
type runningSampleSource struct {
	repo activity.Repository
}

var _ user.RunningSampleSource = (*runningSampleSource)(nil)

func newRunningSampleSource(repo activity.Repository) *runningSampleSource {
	return &runningSampleSource{repo: repo}
}

func (s *runningSampleSource) ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]user.RunningSample, error) {
	rows, err := s.repo.ListRunningSamplesSince(ctx, userID, since)
	if err != nil {
		return nil, err
	}
	out := make([]user.RunningSample, len(rows))
	for i, r := range rows {
		out[i] = user.RunningSample{StartTime: r.StartTime, DistanceMeters: r.DistanceMeters}
	}
	return out, nil
}
