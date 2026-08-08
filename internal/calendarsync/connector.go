package calendarsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
)

// connector turns a user id into the two things any Google Calendar write
// needs: which calendar to write to, and a bearer token to write with.
//
// It was extracted from the planned-workout Service when logged activities
// gained their own sync path. Both services need the identical five-step
// dance — load the connection, reject a revoked one, fetch the encrypted
// refresh token, decrypt it, mint an access token — and both need the same
// subtle failure handling around it (a refresh Google rejects must flip the
// connection to revoked, or the UI never learns to prompt for re-consent).
// Duplicating that across two services would have been a correctness risk,
// not just repetition.
type connector struct {
	conns  calendarconn.Repository
	cipher *tokencrypt.Cipher
	tokens tokenMinter
	now    func() time.Time
}

// grant is everything a Google write needs, plus the connection metadata the
// activity path consults for its no-backfill cutoff.
type grant struct {
	CalendarID  string
	AccessToken string
	Connection  *calendarconn.Connection
}

// resolve returns a usable grant for the user.
//
// ErrNotConnected when there is no connection row; ErrReconnectNeeded when
// the connection is revoked or the refresh fails. Callers treat the two
// differently: the first is "the user never opted in", which is not an error
// condition at all for a background sync, while the second means a grant we
// once had has gone bad and the user must act.
func (c *connector) resolve(ctx context.Context, userID string) (*grant, error) {
	conn, err := c.conns.Get(ctx, userID)
	if errors.Is(err, calendarconn.ErrNotFound) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("calendarsync: get connection: %w", err)
	}
	if conn.Status == calendarconn.StatusRevoked {
		return nil, ErrReconnectNeeded
	}

	enc, nonce, err := c.conns.GetRefreshToken(ctx, userID)
	if errors.Is(err, calendarconn.ErrNotFound) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("calendarsync: get refresh token: %w", err)
	}
	refresh, err := c.cipher.Decrypt(enc, nonce)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: decrypt refresh token: %w", err)
	}

	token, err := c.tokens.Token(ctx, userID, string(refresh))
	if err != nil {
		// A refresh Google rejects (revoked or expired grant) reads as
		// reconnect-needed. Flipping the stored status here is what makes
		// the web UI prompt for re-consent instead of silently retrying a
		// grant that will never work again.
		_ = c.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, c.now())
		return nil, fmt.Errorf("%w: %w", ErrReconnectNeeded, err)
	}

	calendarID := conn.GoogleCalendarID
	if calendarID == "" {
		calendarID = defaultCalendarID
	}
	return &grant{CalendarID: calendarID, AccessToken: token, Connection: conn}, nil
}

// markConnectionSynced advances the connection's durable last-successful-sync
// stamp. Best-effort by design — see calendarconn.Connection for why this
// stamp exists at all (a per-process counter cannot answer "when did this
// last succeed" across restarts) and why failing a user's request over it
// would be the wrong trade.
func (c *connector) markConnectionSynced(ctx context.Context, userID string) {
	_ = c.conns.MarkSynced(ctx, userID, c.now())
}
