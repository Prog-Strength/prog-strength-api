package user

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// statsWeeks is the number of dense weekly buckets the profile-stats series
// carry: the trailing 12 local weeks, oldest first, ending with the week
// containing "now".
const statsWeeks = 12

// LiftSession is the (start, end) projection the stats handler buckets into
// weekly lift-session minutes. It is declared here — not imported from the
// workout package — so the user package stays free of a workout import. The
// server wires a thin adapter that maps workout.SessionDuration → LiftSession.
type LiftSession struct {
	PerformedAt time.Time
	EndedAt     time.Time
}

// RunningSample is the (start, distance) projection the stats handler buckets
// into weekly running distance. Declared here for the same import-isolation
// reason as LiftSession; the server adapts activity.RunSample → RunningSample.
type RunningSample struct {
	StartTime      time.Time
	DistanceMeters float64
}

// LiftSessionSource yields a user's completed lift sessions (those with a known
// end) since a cutoff. Implemented in the wiring layer over the workout repo.
type LiftSessionSource interface {
	ListCompletedSessionsSince(ctx context.Context, userID string, since time.Time) ([]LiftSession, error)
}

// RunningSampleSource yields a user's running activities since a cutoff.
// Implemented in the wiring layer over the activity repo.
type RunningSampleSource interface {
	ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]RunningSample, error)
}

// --- DTOs ----------------------------------------------------------------

// statsPoint is one dense weekly bucket: the Monday-00:00 local boundary that
// opens the week and the summed value for that week.
type statsPoint struct {
	WeekStart time.Time `json:"week_start"`
	Value     float64   `json:"value"`
}

// profileStatsDTO is the GET /users/{username}/stats payload: two dense
// 12-week series. Both are always non-nil slices so they serialize as [] (not
// null), matching the timeline feed's locked-empty convention. Locked is set
// (and the series empty) when the viewer may not see the target's data.
type profileStatsDTO struct {
	LiftSessionMinutes    []statsPoint `json:"lift_session_minutes"`
	RunningDistanceMeters []statsPoint `json:"running_distance_meters"`
	Locked                bool         `json:"locked,omitempty"`
}

// --- pure helpers --------------------------------------------------------

// weekStarts returns the statsWeeks (12) Monday-00:00 boundaries in loc, oldest
// first, ending with the Monday of the local week containing now. It reuses the
// activity package's Monday-based local-week convention (localWeekBounds): a
// week is a user-local calendar concept, so the boundaries are computed in loc,
// not UTC.
func weekStarts(now time.Time, loc *time.Location) []time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	// Go's Weekday() is Sunday=0..Saturday=6; ISO weeks start Monday.
	offset := (int(local.Weekday()) + 6) % 7
	y, mo, d := local.Date()
	thisMonday := time.Date(y, mo, d-offset, 0, 0, 0, 0, loc)

	out := make([]time.Time, statsWeeks)
	for i := 0; i < statsWeeks; i++ {
		// out[0] is the oldest week; out[statsWeeks-1] is thisMonday.
		out[i] = thisMonday.AddDate(0, 0, -7*(statsWeeks-1-i))
	}
	return out
}

// weekly is one (timestamp, value) input to bucketByWeek.
type weekly struct {
	at    time.Time
	value float64
}

// bucketByWeek sums each sample's value into the dense weekly bucket whose
// half-open span [starts[i], starts[i]+7d) contains the sample's timestamp,
// interpreted in loc. Returns a length-statsWeeks slice, zero-filled, with
// week_start set to each boundary. Samples outside the 12-week window are
// dropped (the sources already filter to >= starts[0], but a sample exactly on
// or after the final week's end — e.g. a future-dated row — is simply ignored).
func bucketByWeek(samples []weekly, starts []time.Time, loc *time.Location) []statsPoint {
	if loc == nil {
		loc = time.UTC
	}
	out := make([]statsPoint, len(starts))
	for i := range starts {
		out[i] = statsPoint{WeekStart: starts[i], Value: 0}
	}
	if len(starts) == 0 {
		return out
	}
	end := starts[len(starts)-1].AddDate(0, 0, 7)
	for _, s := range samples {
		t := s.at.In(loc)
		if t.Before(starts[0]) || !t.Before(end) {
			continue
		}
		// Linear scan over 12 buckets is trivially cheap and keeps the
		// boundary semantics (half-open, local) obvious.
		for i := range starts {
			hi := starts[i].AddDate(0, 0, 7)
			if !t.Before(starts[i]) && t.Before(hi) {
				out[i].Value += s.value
				break
			}
		}
	}
	return out
}

// --- handler -------------------------------------------------------------

// getStats handles GET /users/{username}/stats: two dense 12-week weekly series
// — lift-session minutes and running distance in meters — for the target user.
//
// Privacy mirrors the scoped activity feed (timeline.listFeed with ?user=)
// EXACTLY: there is no account-level public/private flag in this product —
// privacy is per-post — so the only way a non-owner sees a user's aggregate
// training data is by being an accepted follower. The gate is therefore: self
// OR accepted-follower (RelationshipFollowing) → full series; anyone else →
// locked. There is deliberately NO public-account bypass, because the data
// model has no such concept; a stranger is always locked, just like the feed
// returns {posts:[], locked:true} for an author they can't see.
func (h *DiscoveryHandler) getStats(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.viewer(w, r)
	if !ok {
		return
	}
	username := chi.URLParam(r, "username")
	target, err := h.repo.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "user not found", "not_found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user by username", err)
		return
	}

	// Privacy gate — mirrors the scoped activity feed. Non-owner viewers must
	// be accepted followers; otherwise the locked-empty state (200) is
	// returned. No public bypass exists (see the doc comment above).
	if target.ID != viewer {
		var rel follow.Relationship
		rel, err = h.follows.Relationship(r.Context(), viewer, target.ID)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "relationship", err)
			return
		}
		if rel != follow.RelationshipFollowing {
			httpresp.OK(w, "got profile stats", profileStatsDTO{
				LiftSessionMinutes:    []statsPoint{},
				RunningDistanceMeters: []statsPoint{},
				Locked:                true,
			})
			return
		}
	}

	loc, err := time.LoadLocation(target.Timezone)
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().UTC()
	starts := weekStarts(now, loc)
	since := starts[0]

	sessions, err := h.lifts.ListCompletedSessionsSince(r.Context(), target.ID, since)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list completed sessions", err)
		return
	}
	runs, err := h.runs.ListRunningSamplesSince(r.Context(), target.ID, since)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list running samples", err)
		return
	}

	liftSamples := make([]weekly, 0, len(sessions))
	for _, s := range sessions {
		mins := s.EndedAt.Sub(s.PerformedAt).Minutes()
		if mins < 0 {
			// A negative span (ended before it started) is degenerate data;
			// clamp to 0 rather than subtracting from the bucket.
			mins = 0
		}
		liftSamples = append(liftSamples, weekly{at: s.PerformedAt, value: mins})
	}
	runSamples := make([]weekly, 0, len(runs))
	for _, rs := range runs {
		runSamples = append(runSamples, weekly{at: rs.StartTime, value: rs.DistanceMeters})
	}

	httpresp.OK(w, "got profile stats", profileStatsDTO{
		LiftSessionMinutes:    bucketByWeek(liftSamples, starts, loc),
		RunningDistanceMeters: bucketByWeek(runSamples, starts, loc),
	})
}
