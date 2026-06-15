package calendarsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// calendarAPIBase is the Google Calendar v3 REST base. It is a package var
// (not const) so tests can repoint it at an httptest.Server without hitting
// Google.
var calendarAPIBase = "https://www.googleapis.com/calendar/v3"

// ErrEventGone is returned by PatchEvent/DeleteEvent when Google reports the
// event no longer exists (HTTP 404 or 410). The service treats this as "drop
// the stored event id and re-insert" rather than a hard failure: a user who
// deletes the event in Google should get a fresh one on the next sync.
var ErrEventGone = errors.New("calendarsync: google event no longer exists")

// ErrTokenRejected is returned when Google rejects the access token (HTTP 401
// or 403). The service flips the connection to revoked and marks the plan
// resyncable so the user can re-consent.
var ErrTokenRejected = errors.New("calendarsync: google rejected the access token")

// GoogleEvent is the minimal event payload we write.
type GoogleEvent struct {
	Summary     string
	Description string
	StartUTC    time.Time
	EndUTC      time.Time
	Timezone    string // IANA; written as the event's time zone
}

// CalendarClient writes events to a user's Google calendar. Fakeable for tests.
type CalendarClient interface {
	InsertEvent(ctx context.Context, accessToken, calendarID string, ev GoogleEvent) (eventID string, err error)
	PatchEvent(ctx context.Context, accessToken, calendarID, eventID string, ev GoogleEvent) error
	DeleteEvent(ctx context.Context, accessToken, calendarID, eventID string) error
}

// eventTime is the Google Calendar {dateTime, timeZone} shape. dateTime is an
// RFC3339 timestamp; timeZone is the IANA zone Google renders it in.
type eventTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone,omitempty"`
}

// eventBody is the JSON request/response body for an events insert/patch.
type eventBody struct {
	ID          string    `json:"id,omitempty"`
	Summary     string    `json:"summary"`
	Description string    `json:"description,omitempty"`
	Start       eventTime `json:"start"`
	End         eventTime `json:"end"`
}

// toBody renders a GoogleEvent into the wire shape. The window is emitted as
// RFC3339 in UTC (with the offset) and the IANA zone is carried separately so
// Google displays the event in the user's local time.
func toBody(ev GoogleEvent) eventBody {
	return eventBody{
		Summary:     ev.Summary,
		Description: ev.Description,
		Start:       eventTime{DateTime: ev.StartUTC.UTC().Format(time.RFC3339), TimeZone: ev.Timezone},
		End:         eventTime{DateTime: ev.EndUTC.UTC().Format(time.RFC3339), TimeZone: ev.Timezone},
	}
}

// googleCalendarClient is the real CalendarClient over the Google Calendar v3
// REST API, using an injected *http.Client (so the timeout/transport is owned
// by the caller). It is stateless and safe for concurrent use.
type googleCalendarClient struct {
	httpClient *http.Client
}

// NewGoogleCalendarClient builds the production CalendarClient. httpClient
// should carry a timeout so a slow Google call can't stall a request.
func NewGoogleCalendarClient(httpClient *http.Client) CalendarClient {
	return &googleCalendarClient{httpClient: httpClient}
}

func (c *googleCalendarClient) InsertEvent(ctx context.Context, accessToken, calendarID string, ev GoogleEvent) (string, error) {
	u := fmt.Sprintf("%s/calendars/%s/events", calendarAPIBase, url.PathEscape(calendarID))
	body, err := json.Marshal(toBody(ev))
	if err != nil {
		return "", fmt.Errorf("calendarsync: marshal event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("calendarsync: build insert request: %w", err)
	}
	resp, err := c.do(req, accessToken)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := classifyStatus(resp.StatusCode); err != nil {
		return "", err
	}
	var out eventBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("calendarsync: decode insert response: %w", err)
	}
	if out.ID == "" {
		return "", errors.New("calendarsync: insert response has no event id")
	}
	return out.ID, nil
}

func (c *googleCalendarClient) PatchEvent(ctx context.Context, accessToken, calendarID, eventID string, ev GoogleEvent) error {
	u := fmt.Sprintf("%s/calendars/%s/events/%s", calendarAPIBase, url.PathEscape(calendarID), url.PathEscape(eventID))
	body, err := json.Marshal(toBody(ev))
	if err != nil {
		return fmt.Errorf("calendarsync: marshal event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("calendarsync: build patch request: %w", err)
	}
	resp, err := c.do(req, accessToken)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return classifyStatus(resp.StatusCode)
}

func (c *googleCalendarClient) DeleteEvent(ctx context.Context, accessToken, calendarID, eventID string) error {
	u := fmt.Sprintf("%s/calendars/%s/events/%s", calendarAPIBase, url.PathEscape(calendarID), url.PathEscape(eventID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("calendarsync: build delete request: %w", err)
	}
	resp, err := c.do(req, accessToken)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return classifyStatus(resp.StatusCode)
}

// do sets the Bearer auth + content type and issues the request. JSON bodies
// always carry Content-Type even on DELETE (which has none) — harmless.
func (c *googleCalendarClient) do(req *http.Request, accessToken string) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if req.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: google calendar request: %w", err)
	}
	return resp, nil
}

// classifyStatus maps a Google response status to (nil | sentinel | generic
// error). 2xx is success; 404/410 → ErrEventGone; 401/403 → ErrTokenRejected;
// anything else is a generic error carrying the status for diagnostics.
func classifyStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound || status == http.StatusGone:
		return ErrEventGone
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrTokenRejected
	default:
		return fmt.Errorf("calendarsync: google calendar returned status %d", status)
	}
}

// TokenSource mints short-lived access tokens from a stored refresh token via
// the calendar oauth2.Config, with a small per-user in-memory cache so a burst
// of writes for one user doesn't hit Google's token endpoint each time.
type TokenSource struct {
	cfg        *oauth2.Config
	httpClient *http.Client
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]cachedToken // userID → token
}

type cachedToken struct {
	accessToken string
	expiry      time.Time
}

// tokenLeeway is how long before real expiry we treat a cached token as stale
// and re-mint, so a token doesn't expire mid-request.
const tokenLeeway = 60 * time.Second

// NewTokenSource builds a TokenSource. cfg is the calendar oauth2.Config (same
// one used for the consent flow). httpClient is injected into the oauth2
// exchange context so tests can target a fake token endpoint. now defaults to
// time.Now when nil.
func NewTokenSource(cfg *oauth2.Config, httpClient *http.Client, now func() time.Time) *TokenSource {
	if now == nil {
		now = time.Now
	}
	return &TokenSource{
		cfg:        cfg,
		httpClient: httpClient,
		now:        now,
		cache:      make(map[string]cachedToken),
	}
}

// Token returns a valid access token for userID, minting one from refreshToken
// via the oauth2 config when the cache is empty or near expiry. The userID is
// only a cache key — the refresh token is the actual credential.
func (s *TokenSource) Token(ctx context.Context, userID, refreshToken string) (string, error) {
	now := s.now()

	s.mu.Lock()
	if ct, ok := s.cache[userID]; ok && ct.expiry.After(now.Add(tokenLeeway)) {
		tok := ct.accessToken
		s.mu.Unlock()
		return tok, nil
	}
	s.mu.Unlock()

	if s.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	}
	tok, err := s.cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return "", fmt.Errorf("calendarsync: mint access token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("calendarsync: token response has no access token")
	}

	expiry := tok.Expiry
	if expiry.IsZero() {
		// oauth2 leaves Expiry zero when the provider omits expires_in; cache
		// for a conservative window so we still amortize, but re-mint soon.
		expiry = now.Add(5 * time.Minute)
	}
	s.mu.Lock()
	s.cache[userID] = cachedToken{accessToken: tok.AccessToken, expiry: expiry}
	s.mu.Unlock()

	return tok.AccessToken, nil
}
