package whoopsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
)

// ErrReconnectNeeded means the user's WHOOP connection is no longer usable
// (not connected, or its refresh token was rejected) and the user must go
// through the OAuth consent flow again. Callers should surface a reconnect
// prompt rather than retrying.
var ErrReconnectNeeded = errors.New("whoopsync: whoop connection needs reconnect")

// ErrUpstream wraps a failure talking to the WHOOP API (fetch of recoveries or
// cycles). Callers that surface HTTP status can map it to 502 rather than 500.
var ErrUpstream = errors.New("whoopsync: whoop api error")

// SyncResult reports what a sync did with the recoveries it fetched. The four
// counts sum to the number of recoveries processed. Returned by SyncSince so an
// operator resync can report the outcome.
type SyncResult struct {
	Upserted        int
	SkippedUnscored int
	SkippedNoCycle  int
	SkippedBadDate  int

	// Sleep is the sleep domain's outcome for the same window. Separate rather
	// than summed: the two domains fail independently, so a single set of
	// counters could not express "recovery landed, sleep 403'd".
	Sleep SleepSyncResult

	// Fetch counts for the combined summary log line only — not part of the
	// operator-facing resync payload.
	recoveriesFetched int
	cyclesFetched     int
}

// SleepSyncResult reports the sleep domain's outcome. There is deliberately no
// SkippedNoCycle: a sleep record carries its own timezone_offset, so the sleep
// path never joins to a cycle and that failure mode cannot occur. Err carries
// the sleep-side failure when the sleep path failed but the overall sync did
// not — the isolation the SOW requires.
//
// Upserted and SkippedUnscored overlap: an unscored record is stored AND
// counted unscored (see syncSleepWindow), so these do not sum to the records
// fetched the way SyncResult's four do.
type SleepSyncResult struct {
	Upserted        int
	SkippedUnscored int
	SkippedBadDate  int
	SkippedNoScope  bool
	Err             error

	fetched int // for the combined summary log line only
}

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

// Sync kinds. These are not cosmetic: kindWindow is the ONLY one that marks the
// connection's durable ingestion-liveness stamp and the only one the
// dead-ingestion alert treats as proof the webhook path is alive. Named
// constants keep that contract from being broken by a rename at one call site
// while the comparison in syncWindow still matches the old literal — a change
// that would silently stop the alert from ever clearing. They double as the
// `kind` metric label.
const (
	kindWindow      = "window"
	kindBackfill    = "backfill"
	kindAdminResync = "admin_resync"
)

// Sync domains. These label syncsTotal so the two independently-failing paths
// are readable apart on one series.
const (
	domainRecovery = "recovery"
	domainSleep    = "sleep"
)

// whoopAPI is the subset of the WHOOP REST client the service needs. Defining
// it here (rather than depending on *Client) lets tests inject a fake.
type whoopAPI interface {
	Recoveries(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Recovery, error)
	Cycles(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Cycle, error)
	Sleeps(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Sleep, error)
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
	sleep      whoopsleep.Repository
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
	sleep whoopsleep.Repository,
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
		sleep:      sleep,
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
	_, err := s.sync(ctx, kindWindow, userID, now.Add(-recentWindow), now, limit)
	return err
}

// Backfill pulls a wider historical window (the last backfillWindow) in one
// shot, used when a connection is first established.
func (s *Service) Backfill(ctx context.Context, userID string) error {
	now := s.now()
	_, err := s.sync(ctx, kindBackfill, userID, now.Add(-backfillWindow), now, backfillLimit)
	return err
}

// SyncSince runs an operator-triggered resync over [now-window, now]. Thin
// wrapper over sync with kindAdminResync — a deliberate label so an
// operator investigating an outage neither increments the window-sync metric
// nor advances the durable liveness stamp the dead-ingestion alert reads.
// Resyncing to diagnose an outage must not silence the alert reporting it.
func (s *Service) SyncSince(ctx context.Context, userID string, window time.Duration, limit int) (SyncResult, error) {
	now := s.now()
	return s.sync(ctx, kindAdminResync, userID, now.Add(-window), now, limit)
}

// sync runs both domain paths over the same window and joins their outcomes.
//
// A recovery failure short-circuits and is returned exactly as it always was:
// nothing landed, and the caller (webhook included) should retry.
//
// A sleep failure is logged, counted, and returned in the SyncResult, but does
// NOT fail the overall sync when recovery succeeded. The webhook consequently
// does not return 500 for a sleep-only failure: a 500 makes WHOOP redeliver
// recovery data we already stored successfully — the same reasoning already
// recorded for MarkWindowSync in syncWindow.
//
// The join lives here rather than being repeated in each of the three public
// entry points so there is exactly one place the isolation rule is expressed.
func (s *Service) sync(ctx context.Context, kind, userID string, start, end time.Time, limit int) (SyncResult, error) {
	res, err := s.syncWindow(ctx, kind, userID, start, end, limit)
	if err != nil {
		return SyncResult{}, err
	}
	res.Sleep = s.syncSleepWindow(ctx, kind, userID, start, end, limit)
	if res.Sleep.Err != nil {
		// Warn, not error: recovery landed, so this is a degraded sync rather
		// than a failed one, and the caller is about to report success.
		slog.WarnContext(ctx, "whoopsync: sleep sync failed; recovery sync unaffected",
			"kind", kind, "user_id", userID, "error", res.Sleep.Err)
	}

	// The one-line answer to "did this user's data actually land, and when?" —
	// upserted=0 on a healthy-looking sync is the first thing to look for when
	// a dashboard is unexpectedly empty. It covers both domains because one
	// webhook nudge drives both, so a per-domain line would only ever be read
	// in pairs.
	attrs := []any{
		"kind", kind,
		"user_id", userID,
		"window_start", start.UTC().Format(time.RFC3339),
		"window_end", end.UTC().Format(time.RFC3339),
		"recoveries_fetched", res.recoveriesFetched,
		"cycles_fetched", res.cyclesFetched,
		"upserted", res.Upserted,
		"skipped_unscored", res.SkippedUnscored,
		"skipped_no_cycle", res.SkippedNoCycle,
		"skipped_bad_date", res.SkippedBadDate,
		"sleeps_fetched", res.Sleep.fetched,
		"sleep_upserted", res.Sleep.Upserted,
		"sleep_skipped_unscored", res.Sleep.SkippedUnscored,
		"sleep_skipped_bad_date", res.Sleep.SkippedBadDate,
		"sleep_skipped_no_scope", res.Sleep.SkippedNoScope,
	}
	if res.Sleep.Err != nil {
		attrs = append(attrs, "sleep_error", res.Sleep.Err)
	}
	slog.InfoContext(ctx, "whoopsync: sync complete", attrs...)
	return res, nil
}

// syncWindow is the shared core: obtain a valid token, fetch recoveries + cycles
// for [start, end], and upsert one recovery entry per SCORED recovery keyed to
// its cycle's local calendar date. Recoveries that are not SCORED, or whose
// cycle is absent from the fetched window, are skipped. kind labels the caller
// (backfill / window) in the summary log and metrics.
func (s *Service) syncWindow(ctx context.Context, kind, userID string, start, end time.Time, limit int) (SyncResult, error) {
	result := "error"
	defer func() { syncsTotal.WithLabelValues(domainRecovery, kind, result).Inc() }()

	accessToken, err := s.validToken(ctx, userID)
	if err != nil {
		return SyncResult{}, err
	}

	recoveries, err := s.api.Recoveries(ctx, accessToken, start, end, limit)
	if err != nil {
		return SyncResult{}, fmt.Errorf("%w: fetch recoveries: %w", ErrUpstream, err)
	}
	cycles, err := s.api.Cycles(ctx, accessToken, start, end, limit)
	if err != nil {
		return SyncResult{}, fmt.Errorf("%w: fetch cycles: %w", ErrUpstream, err)
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
			return SyncResult{}, fmt.Errorf("whoopsync: upsert recovery for %s: %w", date, err)
		}
		upserted++
	}

	syncRowsTotal.WithLabelValues("upserted").Add(float64(upserted))
	syncRowsTotal.WithLabelValues("skipped_unscored").Add(float64(skippedUnscored))
	syncRowsTotal.WithLabelValues("skipped_no_cycle").Add(float64(skippedNoCycle))
	syncRowsTotal.WithLabelValues("skipped_bad_date").Add(float64(skippedBadDate))

	// Durable liveness stamp for the dead-ingestion alert. ONLY kind="window"
	// advances it: backfill runs at connect and admin_resync is an operator
	// action, so neither is evidence the webhook path is alive — the same
	// reasoning that gives SyncSince its own metric label.
	//
	// A failure here does not fail the sync: the recovery rows are already
	// committed, and reporting an error would make the webhook return 500 and
	// have WHOOP redeliver data we successfully stored. It is warned instead,
	// and the alert's own fail-loud direction covers it — a stamp we cannot
	// write eventually reads as stale, which is the safe way to be wrong.
	if kind == kindWindow {
		if err := s.conns.MarkWindowSync(ctx, userID, now); err != nil {
			slog.WarnContext(ctx, "whoopsync: could not record last window sync; ingestion liveness may read stale",
				"user_id", userID, "error", err)
		}
	}

	result = "ok"
	return SyncResult{
		Upserted:          upserted,
		SkippedUnscored:   skippedUnscored,
		SkippedNoCycle:    skippedNoCycle,
		SkippedBadDate:    skippedBadDate,
		recoveriesFetched: len(recoveries),
		cyclesFetched:     len(cycles),
	}, nil
}

// syncSleepWindow fetches sleep records for [start, end] and upserts one row
// per WHOOP sleep record, dated by its END (wake time) localized by the
// record's own timezone_offset.
//
// It is a SIBLING of syncWindow rather than a branch inside it because the two
// domains must fail independently: an under-scoped or degraded sleep fetch can
// never take recovery ingestion down with it. Both go through the same
// validToken, so the per-user keyed mutex still serializes WHOOP's single-use
// refresh-token rotation correctly across both.
//
// It reports its failure in the returned result rather than returning an error,
// because the caller must not propagate it — see sync.
func (s *Service) syncSleepWindow(ctx context.Context, kind, userID string, start, end time.Time, limit int) SleepSyncResult {
	conn, err := s.conns.Get(ctx, userID)
	if err != nil {
		syncsTotal.WithLabelValues(domainSleep, kind, "error").Inc()
		return SleepSyncResult{Err: fmt.Errorf("whoopsync: load connection for sleep sync: %w", err)}
	}
	// Skip the sleep path entirely for a connection that never consented to
	// read:sleep. Calling anyway would 403 on every sync for every under-scoped
	// user, burning rate limit and filling the error metric with a condition
	// that is not an error — it is a user who has not reconnected yet. The skip
	// is deliberately not counted in syncsTotal either: an "ok" there would
	// make the sleep path read as alive while nothing is being fetched, which
	// is exactly the absence-of-success blindness that hid the dead webhook
	// registration for four months.
	if slices.Contains(MissingScopes(conn.Scopes), "read:sleep") {
		sleepScopeSkipsTotal.Inc()
		return SleepSyncResult{SkippedNoScope: true}
	}

	result := "error"
	defer func() { syncsTotal.WithLabelValues(domainSleep, kind, result).Inc() }()

	accessToken, err := s.validToken(ctx, userID)
	if err != nil {
		return SleepSyncResult{Err: err}
	}
	// One endpoint, not two: a sleep record carries its own timezone_offset, so
	// unlike recovery there is no cycle to join for it. That is also why there
	// is no skipped_no_cycle disposition here — the failure mode cannot occur.
	sleeps, err := s.api.Sleeps(ctx, accessToken, start, end, limit)
	if err != nil {
		return SleepSyncResult{Err: fmt.Errorf("%w: fetch sleeps: %w", ErrUpstream, err)}
	}

	res := SleepSyncResult{fetched: len(sleeps)}
	now := s.now()
	for _, r := range sleeps {
		// Date by END (wake time) localized by the record's OWN offset. A night
		// running 22:40 Monday to 06:15 Tuesday is Tuesday's sleep — it is what
		// the user is recovering from when they look at the dashboard on
		// Tuesday, and it is the day the recovery computed from that night
		// lands on. Dating by start is the bug migration 041 exists to correct
		// for recovery; it would additionally put two tiles reading the same
		// night on different days. DST needs no handling: the offset is the one
		// in force at that record's end, as WHOOP recorded it.
		date, err := deriveDate(r.End, r.TimezoneOffset)
		if err != nil {
			slog.WarnContext(ctx, "whoopsync: cannot derive date for sleep; skipping",
				"user_id", userID, "whoop_sleep_id", r.ID, "error", err)
			res.SkippedBadDate++
			continue
		}

		entry := whoopsleep.Entry{
			UserID:         userID,
			WhoopSleepID:   r.ID,
			Date:           date,
			IsNap:          r.Nap,
			StartedAt:      r.Start,
			EndedAt:        r.End,
			TimezoneOffset: r.TimezoneOffset,
			ScoreState:     r.ScoreState,
		}
		if r.ScoreState != "SCORED" {
			// A PENDING or UNSCORABLE record is STILL stored: the row has to
			// exist for the score to land in when WHOOP finishes scoring it.
			// skipped_unscored therefore means "no score to store", not "no row
			// written", and the same record increments upserted below.
			res.SkippedUnscored++
		}
		if r.Score != nil {
			applySleepScore(&entry, r.Score)
		}
		if err := s.sleep.Upsert(ctx, entry, now); err != nil {
			res.Err = fmt.Errorf("whoopsync: upsert sleep %s: %w", r.ID, err)
			break
		}
		res.Upserted++
	}

	sleepRowsTotal.WithLabelValues("upserted").Add(float64(res.Upserted))
	sleepRowsTotal.WithLabelValues("skipped_unscored").Add(float64(res.SkippedUnscored))
	sleepRowsTotal.WithLabelValues("skipped_bad_date").Add(float64(res.SkippedBadDate))

	if res.Err != nil {
		return res
	}
	result = "ok"
	return res
}

// applySleepScore copies WHOOP's scored fields onto the entry. Durations stay
// in the milliseconds WHOOP sent: storing the wire value keeps ingest dumb and
// reversible, and presentation rounding is the tile's job.
func applySleepScore(e *whoopsleep.Entry, score *SleepScore) {
	if st := score.StageSummary; st != nil {
		e.InBedMilli = st.TotalInBedTimeMilli
		e.AwakeMilli = st.TotalAwakeTimeMilli
		e.NoDataMilli = st.TotalNoDataTimeMilli
		e.LightSleepMilli = st.TotalLightSleepTimeMilli
		e.SlowWaveSleepMilli = st.TotalSlowWaveSleepTimeMilli
		e.REMSleepMilli = st.TotalRemSleepTimeMilli
		e.SleepCycleCount = st.SleepCycleCount
		e.DisturbanceCount = st.DisturbanceCount
	}
	if n := score.SleepNeeded; n != nil {
		e.NeedBaselineMilli = n.BaselineMilli
		e.NeedFromSleepDebtMilli = n.NeedFromSleepDebtMilli
		e.NeedFromStrainMilli = n.NeedFromRecentStrainMilli
		e.NeedFromNapMilli = n.NeedFromRecentNapMilli
	}
	e.RespiratoryRate = score.RespiratoryRate
	e.PerformancePct = score.SleepPerformancePercentage
	e.ConsistencyPct = score.SleepConsistencyPercentage
	e.EfficiencyPct = score.SleepEfficiencyPercentage
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
