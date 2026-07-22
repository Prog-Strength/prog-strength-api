package whoopsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// whoopAPIBase is the WHOOP v2 REST base. It is a package var (not const) so
// tests can repoint it at an httptest.Server without hitting WHOOP.
var whoopAPIBase = "https://api.prod.whoop.com"

// maxPages caps how many pages a paginated list call will follow, bounding the
// loop even if the API kept returning a next_token forever.
const maxPages = 10

// ErrRateLimited is returned when WHOOP responds 429. The RetryAfter field
// carries the Retry-After header value if the server sent one.
var ErrRateLimited = errors.New("whoopsync: rate limited by whoop")

// ErrTokenRejected is returned when WHOOP rejects the access token (HTTP 401).
// Callers refresh the token (or flip the connection to revoked) and retry.
var ErrTokenRejected = errors.New("whoopsync: whoop rejected the access token")

// RateLimitError wraps ErrRateLimited with the Retry-After hint. errors.Is
// against ErrRateLimited succeeds via Unwrap.
type RateLimitError struct {
	RetryAfter string // raw Retry-After header value; "" if absent
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter != "" {
		return "whoopsync: rate limited by whoop (retry after " + e.RetryAfter + ")"
	}
	return ErrRateLimited.Error()
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Profile is the WHOOP v2 basic profile.
type Profile struct {
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// RecoveryScore is the scored portion of a recovery record, present only when
// ScoreState is "SCORED".
type RecoveryScore struct {
	RecoveryScore    *float64 `json:"recovery_score"`
	RestingHeartRate *float64 `json:"resting_heart_rate"`
	HRVRmssdMilli    *float64 `json:"hrv_rmssd_milli"`
}

// Recovery is a WHOOP v2 recovery record.
type Recovery struct {
	CycleID    int64          `json:"cycle_id"`
	SleepID    string         `json:"sleep_id"`
	ScoreState string         `json:"score_state"` // SCORED | PENDING | UNSCORABLE
	Score      *RecoveryScore `json:"score"`       // absent when not SCORED
}

// Cycle is a WHOOP v2 physiological cycle.
type Cycle struct {
	ID             int64  `json:"id"`
	Start          string `json:"start"`           // RFC3339
	TimezoneOffset string `json:"timezone_offset"` // e.g. "-08:00"
}

// Client is the WHOOP v2 REST client over an injected *http.Client (so the
// timeout/transport is owned by the caller). It is stateless and safe for
// concurrent use.
type Client struct {
	httpClient *http.Client
}

// NewClient builds the WHOOP client. httpClient should carry a timeout so a
// slow WHOOP call can't stall a request.
func NewClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

// Profile fetches the user's basic profile from GET /v2/user/profile/basic.
func (c *Client) Profile(ctx context.Context, accessToken string) (*Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whoopAPIBase+"/v2/user/profile/basic", nil)
	if err != nil {
		return nil, fmt.Errorf("whoopsync: build profile request: %w", err)
	}
	resp, err := c.do(req, accessToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := classifyStatus(resp); err != nil {
		return nil, err
	}
	var p Profile
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("whoopsync: decode profile: %w", err)
	}
	return &p, nil
}

// recoveryEnvelope is the v2 paginated list wrapper for recoveries.
type recoveryEnvelope struct {
	Records   []Recovery `json:"records"`
	NextToken string     `json:"next_token"`
}

// cycleEnvelope is the v2 paginated list wrapper for cycles.
type cycleEnvelope struct {
	Records   []Cycle `json:"records"`
	NextToken string  `json:"next_token"`
}

// Recoveries fetches recovery records in [start, end], following next_token
// across pages (capped at maxPages).
func (c *Client) Recoveries(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Recovery, error) {
	var all []Recovery
	next := ""
	for page := 0; page < maxPages; page++ {
		var env recoveryEnvelope
		if err := c.getPage(ctx, accessToken, "/v2/recovery", start, end, limit, next, &env); err != nil {
			return nil, err
		}
		all = append(all, env.Records...)
		if env.NextToken == "" {
			break
		}
		next = env.NextToken
	}
	return all, nil
}

// Cycles fetches physiological cycles in [start, end], following next_token
// across pages (capped at maxPages).
func (c *Client) Cycles(ctx context.Context, accessToken string, start, end time.Time, limit int) ([]Cycle, error) {
	var all []Cycle
	next := ""
	for page := 0; page < maxPages; page++ {
		var env cycleEnvelope
		if err := c.getPage(ctx, accessToken, "/v2/cycle", start, end, limit, next, &env); err != nil {
			return nil, err
		}
		all = append(all, env.Records...)
		if env.NextToken == "" {
			break
		}
		next = env.NextToken
	}
	return all, nil
}

// getPage issues one paginated GET against path with the start/end/limit query
// params (and nextToken when non-empty) and decodes the response into out.
func (c *Client) getPage(ctx context.Context, accessToken, path string, start, end time.Time, limit int, nextToken string, out any) error {
	q := url.Values{}
	q.Set("start", start.UTC().Format(time.RFC3339))
	q.Set("end", end.UTC().Format(time.RFC3339))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if nextToken != "" {
		q.Set("nextToken", nextToken)
	}
	u := whoopAPIBase + path + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("whoopsync: build list request: %w", err)
	}
	resp, err := c.do(req, accessToken)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := classifyStatus(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("whoopsync: decode list response: %w", err)
	}
	return nil
}

// do sets the Bearer auth and issues the request.
func (c *Client) do(req *http.Request, accessToken string) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoopsync: whoop request: %w", err)
	}
	return resp, nil
}

// classifyStatus maps a WHOOP response to (nil | sentinel | generic error). 2xx
// is success; 429 → ErrRateLimited (with Retry-After); 401 → ErrTokenRejected;
// anything else is a generic error carrying the status for diagnostics.
func classifyStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: resp.Header.Get("Retry-After")}
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrTokenRejected
	default:
		return fmt.Errorf("whoopsync: whoop returned status %d", resp.StatusCode)
	}
}
