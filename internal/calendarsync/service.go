package calendarsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// ErrNotConnected is returned when a sync is attempted but the user has no
// calendar connection row. It aliases plannedworkout.ErrCalendarNotConnected so
// the planned_workout handler can errors.Is it without importing this package
// (which would create a cycle). The handler maps it to a 4xx prompting the user
// to connect their calendar first.
var ErrNotConnected = plannedworkout.ErrCalendarNotConnected

// ErrReconnectNeeded is returned when the stored grant is no longer usable
// (connection revoked, or Google rejected the token). It aliases
// plannedworkout.ErrCalendarReconnectNeeded (same cycle-avoidance rationale as
// ErrNotConnected). The handler surfaces it as a "reconnect needed" 4xx; the
// plan's sync status is left resyncable so a later Schedule/Resync after
// re-consent recovers.
var ErrReconnectNeeded = plannedworkout.ErrCalendarReconnectNeeded

// tokenMinter mints an access token for a user from a refresh token. *TokenSource
// satisfies it; tests inject a fake.
type tokenMinter interface {
	Token(ctx context.Context, userID, refreshToken string) (string, error)
}

// Service orchestrates Google Calendar writes for planned workouts: load the
// connection, mint a token, render + write the event, and persist the sync
// outcome back onto the plan. Google writes are best-effort — Prog Strength
// stays the source of truth, so a write failure records a "failed" status on
// the plan rather than losing it or 500ing the API call.
type Service struct {
	conns       calendarconn.Repository
	conn        *connector
	client      CalendarClient
	plans       plannedworkout.Repository
	users       user.Repository
	appLinkBase string
	now         func() time.Time
}

// NewService builds the orchestration Service. now defaults to time.Now when
// nil.
func NewService(
	conns calendarconn.Repository,
	cipher *tokencrypt.Cipher,
	tokens *TokenSource,
	client CalendarClient,
	plans plannedworkout.Repository,
	users user.Repository,
	appLinkBase string,
	now func() time.Time,
) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		conns:       conns,
		conn:        &connector{conns: conns, cipher: cipher, tokens: tokens, now: now},
		client:      client,
		plans:       plans,
		users:       users,
		appLinkBase: appLinkBase,
		now:         now,
	}
}

// Schedule inserts-or-patches the Google event for a plan and records sync
// status. detailOverride is optional ("time_block"/"full_agenda"/""); "" means
// use the plan's CalendarDetail, then the user's default.
func (s *Service) Schedule(ctx context.Context, userID, planID, detailOverride string) error {
	plan, err := s.plans.Get(ctx, userID, planID)
	if err != nil {
		return err
	}
	// A completed plan has handed its event to the activity that fulfilled
	// it (see ActivityService.SyncCompletedActivity). Re-rendering the plan
	// over that event would replace "what I actually did" with "what I
	// intended to do" — a strict loss of information, and confusing on a
	// calendar entry the user already saw turn green.
	if handedOver(plan) {
		return nil
	}

	calendarID, accessToken, err := s.connect(ctx, userID)
	if err != nil {
		return err
	}

	detail := EffectiveDetail(plan, detailOverride, s.userDefaultDetail(ctx, userID))
	ev := RenderEvent(plan, detail, s.appLinkBase)
	return s.write(ctx, userID, plan, calendarID, accessToken, ev)
}

// Resync re-attempts the last write for a plan. It is identical to Schedule
// with no override — the render is deterministic from the plan, so re-running
// it repairs a previously failed (or stale) event.
func (s *Service) Resync(ctx context.Context, userID, planID string) error {
	return s.Schedule(ctx, userID, planID, "")
}

// handedOver reports whether a plan has transferred its Google event to the
// logged activity that completed it. Such a plan is no longer the author of
// what that event says.
//
// Completion is the trigger for the handover, so a completed plan with a
// linked session is handed over by definition — including the case where the
// takeover's SetGoogleSync already cleared the id.
func handedOver(plan *plannedworkout.PlannedWorkout) bool {
	return plan != nil &&
		plan.Status == plannedworkout.StatusCompleted &&
		plan.CompletedSessionID != nil && *plan.CompletedSessionID != ""
}

// Delete removes the Google event for a plan, if one was written, then clears
// the stored event id. Missing connection or an already-gone event are not
// errors — the goal is "no orphan event remains".
func (s *Service) Delete(ctx context.Context, userID, planID string) error {
	plan, err := s.plans.Get(ctx, userID, planID)
	if err != nil {
		return err
	}
	if plan.GoogleEventID == nil || *plan.GoogleEventID == "" {
		return nil
	}
	// If the plan handed its event to a completed activity, that activity now
	// owns it. Deleting the plan must not delete the session's event out from
	// under it — clear our pointer and leave Google alone.
	if handedOver(plan) {
		return s.plans.SetGoogleSync(ctx, userID, planID, nil, plannedworkout.SyncPending, nil)
	}

	calendarID, accessToken, err := s.connect(ctx, userID)
	if err != nil {
		// No usable connection: nothing we can do at Google, but clear our
		// stored id so the plan no longer claims a live event.
		_ = s.plans.SetGoogleSync(ctx, userID, planID, nil, plannedworkout.SyncPending, nil)
		if errors.Is(err, ErrNotConnected) {
			return nil
		}
		return err
	}

	if delErr := s.client.DeleteEvent(ctx, accessToken, calendarID, *plan.GoogleEventID); delErr != nil && !errors.Is(delErr, ErrEventGone) {
		if errors.Is(delErr, ErrTokenRejected) {
			_ = s.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, s.now())
		}
		return fmt.Errorf("calendarsync: delete google event: %w", delErr)
	}
	return s.plans.SetGoogleSync(ctx, userID, planID, nil, plannedworkout.SyncPending, nil)
}

// write inserts-or-patches ev and persists the sync outcome onto the plan. It
// centralizes the best-effort failure handling shared by Schedule and
// RewriteCompleted.
func (s *Service) write(ctx context.Context, userID string, plan *plannedworkout.PlannedWorkout, calendarID, accessToken string, ev GoogleEvent) error {
	if plan.GoogleEventID == nil || *plan.GoogleEventID == "" {
		return s.insert(ctx, userID, plan, calendarID, accessToken, ev)
	}

	patchErr := s.client.PatchEvent(ctx, accessToken, calendarID, *plan.GoogleEventID, ev)
	if patchErr == nil {
		eventID := *plan.GoogleEventID
		return s.plans.SetGoogleSync(ctx, userID, plan.ID, &eventID, plannedworkout.SyncSynced, nil)
	}
	if errors.Is(patchErr, ErrEventGone) {
		// The event vanished at Google. Drop our stale id and re-insert a fresh
		// one so the plan ends up synced.
		_ = s.plans.SetGoogleSync(ctx, userID, plan.ID, nil, plannedworkout.SyncPending, nil)
		plan.GoogleEventID = nil
		return s.insert(ctx, userID, plan, calendarID, accessToken, ev)
	}
	return s.recordFailure(ctx, userID, plan, plan.GoogleEventID, patchErr)
}

// insert writes a new event and records the returned id + synced status.
func (s *Service) insert(ctx context.Context, userID string, plan *plannedworkout.PlannedWorkout, calendarID, accessToken string, ev GoogleEvent) error {
	eventID, err := s.client.InsertEvent(ctx, accessToken, calendarID, ev)
	if err != nil {
		return s.recordFailure(ctx, userID, plan, nil, err)
	}
	return s.plans.SetGoogleSync(ctx, userID, plan.ID, &eventID, plannedworkout.SyncSynced, nil)
}

// recordFailure persists a failed sync status (keeping any existing event id)
// and returns the wrapped error. A token rejection additionally flips the
// connection to revoked and is surfaced as ErrReconnectNeeded so the handler
// can prompt re-consent; the plan stays resyncable.
func (s *Service) recordFailure(ctx context.Context, userID string, plan *plannedworkout.PlannedWorkout, eventID *string, cause error) error {
	msg := cause.Error()
	_ = s.plans.SetGoogleSync(ctx, userID, plan.ID, eventID, plannedworkout.SyncFailed, &msg)

	if errors.Is(cause, ErrTokenRejected) {
		_ = s.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, s.now())
		return fmt.Errorf("%w: %w", ErrReconnectNeeded, cause)
	}
	return fmt.Errorf("calendarsync: write google event: %w", cause)
}

// connect resolves the user's calendar id and an access token. The work lives
// in the shared connector, which the activity sync path uses identically.
func (s *Service) connect(ctx context.Context, userID string) (calendarID, accessToken string, err error) {
	g, err := s.conn.resolve(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return g.CalendarID, g.AccessToken, nil
}

// userDefaultDetail reads the user's CalendarDefaultDetail, returning "" on any
// lookup error so EffectiveDetail falls back to time_block rather than failing
// the sync.
func (s *Service) userDefaultDetail(ctx context.Context, userID string) string {
	if s.users == nil {
		return ""
	}
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return ""
	}
	return u.CalendarDefaultDetail
}
