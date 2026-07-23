package whoopsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// ErrReconnectNeeded means the user's WHOOP connection is no longer usable
// (not connected, or its refresh token was rejected) and the user must go
// through the OAuth consent flow again. Callers should surface a reconnect
// prompt rather than retrying.
var ErrReconnectNeeded = errors.New("whoopsync: whoop connection needs reconnect")

// refreshSkew is how far ahead of the recorded expiry we proactively refresh.
// Refreshing a little early avoids racing an about-to-expire access token
// against a slow WHOOP API call.
const refreshSkew = 2 * time.Minute

// recentWindow is the lookback for SyncWindow (webhook-triggered recent sync);
// comfortably covers the handful of recoveries a webhook nudge is about.
const recentWindow = 7 * 24 * time.Hour

// backfillWindow / backfillLimit bound the one-shot historical backfill run.
const (
	backfillWindow = 30 * 24 * time.Hour
	backfillLimit  = 25
)

// whoopAPI is the subset of the WHOOP REST client the service needs. Defining
// it here (rather than depending on *Client) lets tests inject a fake.
type whoopAPI interface {
	Recoveries(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Recovery, error)
	Cycles(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Cycle, error)
}

// tokenRefresher is the subset of OAuthConfig the service needs (only the
// refresh grant). Defining it here lets tests inject a fake refresher.
type tokenRefresher interface {
	Refresh(ctx context.Context, httpClient *http.Client, refreshToken string) (*Tokens, error)
}

// Compile-time checks that the production concrete types satisfy the service's
// narrow interfaces (so wiring *Client / *OAuthConfig in production can't drift).
var (
	_ whoopAPI       = (*Client)(nil)
	_ tokenRefresher = (*OAuthConfig)(nil)
)

// Service syncs WHOOP recovery data into the local store. It owns the
// token-lifecycle logic (decrypt, proactive refresh with single-use rotation)
// and the recovery→date mapping, keeping the HTTP client and repositories
// injected so it is testable with fakes.
type Service struct {
	conns      whoopconn.Repository
	rec        whooprecovery.Repository
	cipher     *tokencrypt.Cipher
	api        whoopAPI
	oauth      tokenRefresher
	httpClient *http.Client
	now        func() time.Time
	mu         *keyedMutex
}

// NewService wires the sync service. now defaults to time.Now when nil.
func NewService(
	conns whoopconn.Repository,
	rec whooprecovery.Repository,
	cipher *tokencrypt.Cipher,
	api whoopAPI,
	oauth tokenRefresher,
	httpClient *http.Client,
	now func() time.Time,
) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		conns:      conns,
		rec:        rec,
		cipher:     cipher,
		api:        api,
		oauth:      oauth,
		httpClient: httpClient,
		now:        now,
		mu:         newKeyedMutex(),
	}
}

// SyncWindow syncs a recent window (the last recentWindow up to now) for the
// user. limit is passed to WHOOP's list endpoints (a webhook nudge passes a
// small value covering the handful of recoveries it announced).
func (s *Service) SyncWindow(ctx context.Context, userID string, limit int) error {
	now := s.now()
	return s.syncWindow(ctx, "window", userID, now.Add(-recentWindow), now, limit)
}

// Backfill pulls a wider historical window (the last backfillWindow) in one
// shot, used when a connection is first established.
func (s *Service) Backfill(ctx context.Context, userID string) error {
	now := s.now()
	return s.syncWindow(ctx, "backfill", userID, now.Add(-backfillWindow), now, backfillLimit)
}

// syncWindow is the shared core: obtain a valid token, fetch recoveries + cycles
// for [start, end], and upsert one recovery entry per SCORED recovery keyed to
// its cycle's local calendar date. Recoveries that are not SCORED, or whose
// cycle is absent from the fetched window, are skipped. kind labels the caller
// (backfill / window) in the summary log and metrics.
func (s *Service) syncWindow(ctx context.Context, kind, userID string, start, end time.Time, limit int) error {
	result := "error"
	defer func() { syncsTotal.WithLabelValues(kind, result).Inc() }()

	accessToken, err := s.validToken(ctx, userID)
	if err != nil {
		return err
	}

	recoveries, err := s.api.Recoveries(ctx, accessToken, start, end, limit)
	if err != nil {
		return fmt.Errorf("whoopsync: fetch recoveries: %w", err)
	}
	cycles, err := s.api.Cycles(ctx, accessToken, start, end, limit)
	if err != nil {
		return fmt.Errorf("whoopsync: fetch cycles: %w", err)
	}

	byID := make(map[int64]Cycle, len(cycles))
	for _, c := range cycles {
		byID[c.ID] = c
	}

	var upserted, skippedUnscored, skippedNoCycle, skippedBadDate int
	now := s.now()
	for _, r := range recoveries {
		if r.ScoreState != "SCORED" {
			skippedUnscored++ // PENDING / UNSCORABLE: no metrics to store yet.
			continue
		}
		cycle, ok := byID[r.CycleID]
		if !ok {
			// The recovery references a cycle outside the fetched window; skip
			// it rather than guessing a date. A later window covering the cycle
			// will pick it up.
			slog.WarnContext(ctx, "whoopsync: recovery has no matching cycle in window; skipping",
				"user_id", userID, "cycle_id", r.CycleID, "sleep_id", r.SleepID)
			skippedNoCycle++
			continue
		}
		// Date the recovery by WHEN IT WAS SCORED (created_at ≈ wake + phone
		// sync), localized by the cycle's timezone_offset — NOT by the cycle's
		// start. WHOOP cycles run sleep-onset → sleep-onset, so cycle.start is
		// the previous evening's bedtime and dating by it pins every recovery
		// to the day BEFORE the user woke up with it ("today" never has data).
		// The cycle fetch stays: it is the only source of timezone_offset.
		date, err := deriveDate(r.CreatedAt, cycle.TimezoneOffset)
		if err != nil {
			slog.WarnContext(ctx, "whoopsync: cannot derive date for recovery; skipping",
				"user_id", userID, "cycle_id", r.CycleID, "error", err)
			skippedBadDate++
			continue
		}

		entry := whooprecovery.Entry{
			UserID:  userID,
			Date:    date,
			CycleID: r.CycleID,
			SleepID: r.SleepID,
		}
		if r.Score != nil {
			entry.RecoveryScore = r.Score.RecoveryScore
			entry.RestingHeartRate = r.Score.RestingHeartRate
			entry.HRVRmssdMilli = r.Score.HRVRmssdMilli
		}
		if err := s.rec.Upsert(ctx, entry, now); err != nil {
			return fmt.Errorf("whoopsync: upsert recovery for %s: %w", date, err)
		}
		upserted++
	}

	syncRowsTotal.WithLabelValues("upserted").Add(float64(upserted))
	syncRowsTotal.WithLabelValues("skipped_unscored").Add(float64(skippedUnscored))
	syncRowsTotal.WithLabelValues("skipped_no_cycle").Add(float64(skippedNoCycle))
	syncRowsTotal.WithLabelValues("skipped_bad_date").Add(float64(skippedBadDate))

	// The one-line answer to "did this user's data actually land, and when?" —
	// upserted=0 on a healthy-looking sync is the first thing to look for when
	// a dashboard is unexpectedly empty.
	slog.InfoContext(ctx, "whoopsync: sync complete",
		"kind", kind,
		"user_id", userID,
		"window_start", start.UTC().Format(time.RFC3339),
		"window_end", end.UTC().Format(time.RFC3339),
		"recoveries_fetched", len(recoveries),
		"cycles_fetched", len(cycles),
		"upserted", upserted,
		"skipped_unscored", skippedUnscored,
		"skipped_no_cycle", skippedNoCycle,
		"skipped_bad_date", skippedBadDate,
	)
	result = "ok"
	return nil
}

// validToken returns a usable access token for the user, refreshing it first if
// it is at or near expiry. The whole read-decrypt-maybe-refresh-persist
// sequence is serialized per user so two concurrent syncs can't both consume
// the single-use refresh token.
func (s *Service) validToken(ctx context.Context, userID string) (string, error) {
	s.mu.Lock(userID)
	defer s.mu.Unlock(userID)

	conn, err := s.conns.Get(ctx, userID)
	if err != nil {
		if errors.Is(err, whoopconn.ErrNotFound) {
			return "", fmt.Errorf("%w: no connection", ErrReconnectNeeded)
		}
		return "", fmt.Errorf("whoopsync: load connection: %w", err)
	}
	if conn.Status != whoopconn.StatusConnected {
		return "", fmt.Errorf("%w: status %s", ErrReconnectNeeded, conn.Status)
	}

	bundle, err := s.conns.GetTokens(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("whoopsync: load tokens: %w", err)
	}
	accessToken, err := s.cipher.Decrypt(bundle.AccessTokenEnc, bundle.AccessTokenNonce)
	if err != nil {
		return "", fmt.Errorf("whoopsync: decrypt access token: %w", err)
	}

	if s.now().Add(refreshSkew).Before(bundle.ExpiresAt) {
		// Comfortably before expiry: current token is fine.
		return string(accessToken), nil
	}
	return s.refresh(ctx, userID, bundle)
}

// refresh performs the single-use refresh rotation: it exchanges the stored
// refresh token for a new pair, PERSISTS the encrypted new pair before
// returning it, and only then hands the new access token back. Persisting
// before use means a crash after WHOOP rotated the token can't orphan the
// connection with a stale (now-invalid) refresh token on file.
func (s *Service) refresh(ctx context.Context, userID string, bundle *whoopconn.TokenBundle) (string, error) {
	refreshToken, err := s.cipher.Decrypt(bundle.RefreshTokenEnc, bundle.RefreshTokenNonce)
	if err != nil {
		return "", fmt.Errorf("whoopsync: decrypt refresh token: %w", err)
	}

	tokens, err := s.oauth.Refresh(ctx, s.httpClient, string(refreshToken))
	if err != nil {
		if errors.Is(err, ErrInvalidGrant) {
			// The refresh token is dead (revoked at WHOOP, or a lost rotation).
			// This is the transition behind every "my WHOOP stopped syncing"
			// report — log it as the state change it is, not just an error.
			slog.WarnContext(ctx, "whoopsync: refresh rejected; connection flipped to error, reconnect required",
				"user_id", userID)
			tokenRefreshesTotal.WithLabelValues("invalid_grant").Inc()
			if serr := s.conns.SetStatus(ctx, userID, whoopconn.StatusError, s.now()); serr != nil {
				return "", fmt.Errorf("whoopsync: set status error after invalid grant: %w", serr)
			}
			return "", fmt.Errorf("%w: %w", ErrReconnectNeeded, err)
		}
		tokenRefreshesTotal.WithLabelValues("error").Inc()
		return "", fmt.Errorf("whoopsync: refresh token: %w", err)
	}

	accessEnc, accessNonce, err := s.cipher.Encrypt([]byte(tokens.AccessToken))
	if err != nil {
		tokenRefreshesTotal.WithLabelValues("persist_error").Inc()
		return "", fmt.Errorf("whoopsync: encrypt new access token: %w", err)
	}
	refreshEnc, refreshNonce, err := s.cipher.Encrypt([]byte(tokens.RefreshToken))
	if err != nil {
		tokenRefreshesTotal.WithLabelValues("persist_error").Inc()
		return "", fmt.Errorf("whoopsync: encrypt new refresh token: %w", err)
	}

	rotated := whoopconn.TokenBundle{
		AccessTokenEnc:    accessEnc,
		AccessTokenNonce:  accessNonce,
		RefreshTokenEnc:   refreshEnc,
		RefreshTokenNonce: refreshNonce,
		ExpiresAt:         tokens.ExpiresAt,
	}
	// Persist BEFORE returning the new access token (single-use rotation).
	if err := s.conns.UpdateTokens(ctx, userID, rotated, s.now()); err != nil {
		// WHOOP already rotated: the old refresh token is now invalid and the
		// new one wasn't stored. The connection is likely orphaned — the next
		// refresh will invalid_grant and force a reconnect. Log accordingly.
		slog.ErrorContext(ctx, "whoopsync: rotated tokens not persisted; connection likely orphaned until reconnect",
			"user_id", userID, "error", err)
		tokenRefreshesTotal.WithLabelValues("persist_error").Inc()
		return "", fmt.Errorf("whoopsync: persist rotated tokens: %w", err)
	}
	slog.InfoContext(ctx, "whoopsync: token refreshed", "user_id", userID)
	tokenRefreshesTotal.WithLabelValues("ok").Inc()
	return tokens.AccessToken, nil
}

// deriveDate maps a UTC RFC3339 instant (in practice the recovery's created_at)
// to the local calendar date (YYYY-MM-DD) implied by tzOffset (the cycle's
// timezone_offset). The offset is authoritative — no IANA lookup — so
// DST-adjacent days format correctly from the raw offset. It is
// exported-for-test as a pure helper.
func deriveDate(instantRFC3339, tzOffset string) (string, error) {
	instant, err := time.Parse(time.RFC3339, instantRFC3339)
	if err != nil {
		return "", fmt.Errorf("whoopsync: parse instant %q: %w", instantRFC3339, err)
	}
	offset, err := parseOffset(tzOffset)
	if err != nil {
		return "", err
	}
	zone := time.FixedZone(tzOffset, int(offset/time.Second))
	return instant.In(zone).Format("2006-01-02"), nil
}

// parseOffset parses a WHOOP timezone_offset like "-08:00" or "+11:00" into a
// signed duration. It rejects anything not in [+-]HH:MM form.
func parseOffset(off string) (time.Duration, error) {
	if len(off) != 6 || (off[0] != '+' && off[0] != '-') || off[3] != ':' {
		return 0, fmt.Errorf("whoopsync: bad timezone offset %q", off)
	}
	hh, err := strconv.Atoi(off[1:3])
	if err != nil {
		return 0, fmt.Errorf("whoopsync: bad timezone offset %q: %w", off, err)
	}
	mm, err := strconv.Atoi(off[4:6])
	if err != nil {
		return 0, fmt.Errorf("whoopsync: bad timezone offset %q: %w", off, err)
	}
	if hh > 23 || mm > 59 {
		return 0, fmt.Errorf("whoopsync: timezone offset out of range %q", off)
	}
	d := time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute
	if off[0] == '-' {
		d = -d
	}
	return d, nil
}
