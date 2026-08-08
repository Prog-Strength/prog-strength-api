package calendarsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// ErrSyncSkipped reports that no write was attempted because policy said not
// to — the activity predates the user's connection (the no-backfill cutoff),
// or its type has no calendar renderer.
//
// It is deliberately distinct from both success and failure. Recording a
// skip as a failure would make the dashboard's failed-sync panel fire on
// completely correct behavior, and recording it as a success would advance
// the liveness stamp for a write that never happened — which is exactly the
// blindness the WHOOP outage was made of.
var ErrSyncSkipped = errors.New("calendarsync: sync skipped by policy")

// activityReader is the narrow read surface the sync path needs from the
// activity domain.
type activityReader interface {
	Get(ctx context.Context, userID, activityID string) (*activity.Activity, error)
	BestEffortsForActivity(ctx context.Context, userID, activityID string) ([]activity.ActivityBestEffort, error)
}

// planLookup is the narrow planned-workout surface used for event takeover.
type planLookup interface {
	GetByCompletedSession(ctx context.Context, userID, sessionID string) (*plannedworkout.PlannedWorkout, error)
	SetGoogleSync(ctx context.Context, userID, id string, eventID *string, status plannedworkout.GoogleSyncStatus, lastErr *string) error
}

// ActivityService syncs LOGGED activities to Google Calendar.
//
// It is the completed-session counterpart of Service (which owns forward-
// looking planned workouts), and it shares that type's contract: Prog Strength
// is the source of truth, Google writes are best-effort, and a failure records
// a resyncable status rather than failing the user's request or losing data.
type ActivityService struct {
	conn       *connector
	client     CalendarClient
	activities activityReader
	registry   *activity.Registry
	state      ActivitySyncRepository
	plans      planLookup
	users      user.Repository
	appLink    string
	now        func() time.Time
	// observer receives the outcome of every attempt, for metrics. Optional.
	observer SyncObserver
}

// SyncObserver is notified of each sync outcome so the metrics layer can
// count them without this package importing Prometheus.
type SyncObserver interface {
	ObserveSync(activityType, trigger, result string)
}

// ActivityServiceDeps groups ActivityService's collaborators. It is a struct
// rather than a nine-argument constructor because the wiring in server.go is
// already dense, and positional arguments of the same type (three string-ish
// repositories) are easy to transpose silently.
type ActivityServiceDeps struct {
	Conns       calendarconn.Repository
	Cipher      *tokencrypt.Cipher
	Tokens      *TokenSource
	Client      CalendarClient
	Activities  activityReader
	Registry    *activity.Registry
	State       ActivitySyncRepository
	Plans       planLookup
	Users       user.Repository
	AppLinkBase string
	Now         func() time.Time
	Observer    SyncObserver
}

func NewActivityService(d ActivityServiceDeps) *ActivityService {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &ActivityService{
		conn:       &connector{conns: d.Conns, cipher: d.Cipher, tokens: d.Tokens, now: now},
		client:     d.Client,
		activities: d.Activities,
		registry:   d.Registry,
		state:      d.State,
		plans:      d.Plans,
		users:      d.Users,
		appLink:    d.AppLinkBase,
		now:        now,
		observer:   d.Observer,
	}
}

// SyncActivity writes (or rewrites) the Google event for one logged activity.
//
// It is idempotent: calling it repeatedly patches the same event rather than
// accumulating duplicates, which is what lets the create hook, the edit hook,
// and the boot reconciler all call the same method without coordinating.
func (s *ActivityService) SyncActivity(ctx context.Context, userID, activityID string) error {
	return s.sync(ctx, userID, activityID, "inline")
}

// ReconcileActivity is SyncActivity tagged as reconciler-driven, so metrics
// can distinguish a repair from a live write. A dashboard where these are
// indistinguishable cannot answer "is the inline path broken?" — the
// reconciler would quietly paper over it, which is the failure this whole
// design is built to avoid.
func (s *ActivityService) ReconcileActivity(ctx context.Context, userID, activityID string) error {
	return s.sync(ctx, userID, activityID, "reconcile")
}

func (s *ActivityService) sync(ctx context.Context, userID, activityID, trigger string) (err error) {
	a, err := s.activities.Get(ctx, userID, activityID)
	if err != nil {
		return fmt.Errorf("calendarsync: load activity: %w", err)
	}
	// Observe against the real type once it is known, so the dashboard can
	// break failures down by sport.
	defer func() { s.observe(string(a.ActivityType), trigger, err) }()

	g, err := s.conn.resolve(ctx, userID)
	if err != nil {
		// Never connected is not a problem to report — the overwhelming
		// majority of activities belong to users who never opted in.
		if errors.Is(err, ErrNotConnected) {
			return ErrSyncSkipped
		}
		return err
	}
	if !a.CreatedAt.After(g.Connection.ConnectedAt) {
		// The no-backfill cutoff. Deliberately created_at, not start_time:
		// "everything you log from now on".
		return ErrSyncSkipped
	}

	ev, ok := s.render(ctx, userID, a)
	if !ok {
		return ErrSyncSkipped
	}
	return s.write(ctx, userID, a, g, ev)
}

// render builds the Google event for an activity, or ok=false when its type
// has no manifest (an unregistered type — RenderManifest already falls back
// to the card summary for everything else).
func (s *ActivityService) render(ctx context.Context, userID string, a *activity.Activity) (GoogleEvent, bool) {
	// Best efforts are not loaded by the activity read, so fetch them for
	// the types that have them. A failure here degrades the body rather than
	// failing the sync — a run's event without its splits is still correct.
	if efforts, err := s.activities.BestEffortsForActivity(ctx, userID, a.ID); err == nil {
		a.BestEfforts = efforts
	}

	m, ok := activity.RenderManifest(s.registry, *a, s.loadDetails(ctx, userID, a))
	if !ok {
		return GoogleEvent{}, false
	}
	return RenderActivityEvent(a, m, s.userTimezone(ctx, userID), s.appLink), true
}

// loadDetails fetches the type's detail payload for the renderer. Best-effort:
// a nil return degrades every renderer to a base-row event, which is the
// documented contract of both Summarize and CalendarEvent.
func (s *ActivityService) loadDetails(ctx context.Context, userID string, a *activity.Activity) any {
	if s.registry == nil {
		return nil
	}
	d, err := s.registry.Lookup(a.ActivityType)
	if err != nil || d.Details == nil {
		return nil
	}
	details, err := d.Details.Load(ctx, userID, a.ID)
	if err != nil {
		return nil
	}
	return details
}

// write inserts or patches the event and records the outcome.
func (s *ActivityService) write(ctx context.Context, userID string, a *activity.Activity, g *grant, ev GoogleEvent) error {
	eventID := s.existingEventID(ctx, userID, a.ID)
	if eventID == nil {
		return s.insert(ctx, userID, a.ID, g, ev)
	}

	err := s.client.PatchEvent(ctx, g.AccessToken, g.CalendarID, *eventID, ev)
	if err == nil {
		return s.recordSuccess(ctx, userID, a.ID, *eventID)
	}
	if errors.Is(err, ErrEventGone) {
		// The user deleted the event in Google. Drop the stale id and write
		// a fresh one, so the calendar converges back on what we believe.
		_ = s.state.Release(ctx, userID, a.ID, s.now())
		return s.insert(ctx, userID, a.ID, g, ev)
	}
	return s.recordFailure(ctx, userID, a.ID, eventID, err)
}

func (s *ActivityService) insert(ctx context.Context, userID, activityID string, g *grant, ev GoogleEvent) error {
	eventID, err := s.client.InsertEvent(ctx, g.AccessToken, g.CalendarID, ev)
	if err != nil {
		return s.recordFailure(ctx, userID, activityID, nil, err)
	}
	return s.recordSuccess(ctx, userID, activityID, eventID)
}

// existingEventID returns the Google event this activity should write to.
//
// The second branch is the plan takeover. When an activity completed a
// planned workout, that plan may already own an event on the calendar — the
// forward-looking time block the user scheduled. Adopting its id turns that
// event INTO the completed session (one entry that evolves) rather than
// leaving a planned block sitting next to a duplicate "✓" event for the same
// session. Both rows then reference the same event, which is safe because
// Google's 404/410 makes the eventual double-delete a no-op.
func (s *ActivityService) existingEventID(ctx context.Context, userID, activityID string) *string {
	if st, err := s.state.Get(ctx, userID, activityID); err == nil && st.GoogleEventID != nil && *st.GoogleEventID != "" {
		return st.GoogleEventID
	}
	if s.plans == nil {
		return nil
	}
	plan, err := s.plans.GetByCompletedSession(ctx, userID, activityID)
	if err != nil || plan == nil || plan.GoogleEventID == nil || *plan.GoogleEventID == "" {
		return nil
	}
	return plan.GoogleEventID
}

// recordSuccess persists the synced state and advances the durable liveness
// stamp the alert reads.
func (s *ActivityService) recordSuccess(ctx context.Context, userID, activityID, eventID string) error {
	if err := s.state.MarkSynced(ctx, userID, activityID, eventID, s.now()); err != nil {
		return fmt.Errorf("calendarsync: record sync state: %w", err)
	}
	s.conn.markConnectionSynced(ctx, userID)
	return nil
}

// recordFailure persists a failed, resyncable state and returns the wrapped
// error. A token rejection additionally flips the connection to revoked and
// surfaces as ErrReconnectNeeded so the UI can prompt re-consent.
func (s *ActivityService) recordFailure(ctx context.Context, userID, activityID string, eventID *string, cause error) error {
	_ = s.state.MarkFailed(ctx, userID, activityID, eventID, cause.Error(), s.now())

	if errors.Is(cause, ErrTokenRejected) {
		_ = s.conn.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, s.now())
		return fmt.Errorf("%w: %w", ErrReconnectNeeded, cause)
	}
	return fmt.Errorf("calendarsync: write activity event: %w", cause)
}

// DeleteActivityEvent removes the Google event for an activity, if one was
// written, and clears the stored id.
//
// A missing connection or an already-gone event are not errors: the goal is
// "no orphan event remains", and both of those already satisfy it.
func (s *ActivityService) DeleteActivityEvent(ctx context.Context, userID, activityID string) error {
	st, err := s.state.Get(ctx, userID, activityID)
	if errors.Is(err, ErrSyncStateNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("calendarsync: load sync state: %w", err)
	}
	if st.GoogleEventID == nil || *st.GoogleEventID == "" {
		return nil
	}

	g, err := s.conn.resolve(ctx, userID)
	if err != nil {
		// No usable grant: nothing can be done at Google, but our row must
		// stop claiming a live event either way.
		_ = s.state.Release(ctx, userID, activityID, s.now())
		if errors.Is(err, ErrNotConnected) {
			return nil
		}
		return err
	}

	if err := s.client.DeleteEvent(ctx, g.AccessToken, g.CalendarID, *st.GoogleEventID); err != nil && !errors.Is(err, ErrEventGone) {
		if errors.Is(err, ErrTokenRejected) {
			_ = s.conn.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, s.now())
		}
		return fmt.Errorf("calendarsync: delete activity event: %w", err)
	}
	return s.state.Release(ctx, userID, activityID, s.now())
}

// SyncCompletedActivity is the planned-workout completion hook. The plan
// hands its event over to the session that fulfilled it, so the calendar
// shows one entry moving from planned to done.
//
// It replaces the old RewriteCompleted, which rendered completed sessions
// from a caller-supplied text blob. Routing completion through the same
// per-type renderer as every other logged activity means a completed lift on
// the calendar looks exactly like a spontaneous one — there is no longer a
// second rendering path to drift.
func (s *ActivityService) SyncCompletedActivity(ctx context.Context, userID, planID, activityID string) error {
	err := s.SyncActivity(ctx, userID, activityID)
	if err != nil && !errors.Is(err, ErrSyncSkipped) {
		return err
	}

	// The activity now owns the event. Clear the plan's pointer so a later
	// plan-side Resync cannot overwrite the completed body with the original
	// planned agenda, and so deleting the plan does not delete the session's
	// event out from under it.
	if s.plans != nil {
		_ = s.plans.SetGoogleSync(ctx, userID, planID, nil, plannedworkout.SyncSynced, nil)
	}
	return nil
}

// userTimezone returns the user's IANA zone, or "" when unknown — Google then
// renders the event against the viewing calendar's own zone, which for an
// absolute instant is still correct, just not necessarily the zone the
// session was performed in.
func (s *ActivityService) userTimezone(ctx context.Context, userID string) string {
	if s.users == nil {
		return ""
	}
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return ""
	}
	return u.Timezone
}

// observe reports one attempt's outcome to the metrics layer.
func (s *ActivityService) observe(activityType, trigger string, err error) {
	if s.observer == nil {
		return
	}
	switch {
	case err == nil:
		s.observer.ObserveSync(activityType, trigger, "synced")
	case errors.Is(err, ErrSyncSkipped):
		s.observer.ObserveSync(activityType, trigger, "skipped")
	case errors.Is(err, ErrReconnectNeeded):
		s.observer.ObserveSync(activityType, trigger, "reconnect_needed")
	default:
		s.observer.ObserveSync(activityType, trigger, "failed")
	}
}
