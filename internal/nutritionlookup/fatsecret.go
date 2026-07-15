package nutritionlookup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FatSecret Platform API provider (Basic edition).
//
// Primary lookup source because the Basic (free) tier includes US
// restaurant and branded foods — exactly the gap USDA can't cover.
// Auth is OAuth2 client-credentials; tokens are cached in-process
// until shortly before expiry so a lookup normally costs one HTTP call.
//
// Macros are parsed out of food_description on the search response
// ("Per 1 sandwich - Calories: 440kcal | Fat: 19.00g | Carbs: 41.00g |
// Protein: 28.00g") instead of N+1 food.get calls per candidate — one
// round trip per lookup, and the description format is a stable,
// documented part of foods.search. Candidates whose description
// doesn't parse are skipped rather than guessed at.

const (
	// G101 fires on the "secret"/"token" substrings in these names —
	// they're public endpoint URLs, not credentials.
	fatSecretTokenURL = "https://oauth.fatsecret.com/connect/token"      //nolint:gosec // G101: public URL, not a credential
	fatSecretAPIURL   = "https://platform.fatsecret.com/rest/server.api" //nolint:gosec // G101: public URL, not a credential

	// Refresh the cached token this many seconds before its stated
	// expiry so an in-flight search never races an expiring token.
	tokenRefreshMargin = 60 * time.Second
)

// "Per 1 sandwich - Calories: 440kcal | Fat: 19.00g | Carbs: 41.00g | Protein: 28.00g"
var fatSecretDescriptionRE = regexp.MustCompile(
	`(?i)Per\s+(?P<serving>.+?)\s*-\s*` +
		`Calories:\s*(?P<calories>[\d.]+)\s*kcal\s*\|\s*` +
		`Fat:\s*(?P<fat>[\d.]+)\s*g\s*\|\s*` +
		`Carbs:\s*(?P<carbs>[\d.]+)\s*g\s*\|\s*` +
		`Protein:\s*(?P<protein>[\d.]+)\s*g`,
)

// Compile-time check that *FatSecretProvider satisfies Provider.
var _ Provider = (*FatSecretProvider)(nil)

type FatSecretProvider struct {
	client       *http.Client
	clientID     string
	clientSecret string
	log          *slog.Logger

	// TokenURL and APIURL default to the production FatSecret
	// endpoints; tests point them at httptest servers.
	TokenURL string
	APIURL   string

	// mu guards the cached token. Search runs concurrently across
	// requests; only one caller should refresh an expired token.
	mu             sync.Mutex
	token          string
	tokenExpiresAt time.Time
}

func NewFatSecretProvider(client *http.Client, clientID, clientSecret string, log *slog.Logger) *FatSecretProvider {
	return &FatSecretProvider{
		client:       client,
		clientID:     clientID,
		clientSecret: clientSecret,
		log:          log,
		TokenURL:     fatSecretTokenURL,
		APIURL:       fatSecretAPIURL,
	}
}

func (p *FatSecretProvider) Source() string { return "fatsecret" }

func (p *FatSecretProvider) Configured() bool {
	return p.clientID != "" && p.clientSecret != ""
}

func (p *FatSecretProvider) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	token, err := p.getToken(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{
		"method":            {"foods.search"},
		"search_expression": {query},
		"format":            {"json"},
		"max_results":       {strconv.Itoa(limit)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.APIURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	started := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	p.log.DebugContext(ctx, "fatsecret foods.search response",
		"status", resp.StatusCode, "elapsed_ms", time.Since(started).Milliseconds())
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fatsecret search: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	hits, stats, err := parseFatSecretSearch(ctx, p.log, body, limit)
	if err != nil {
		return nil, err
	}
	p.log.InfoContext(ctx, "fatsecret search parsed",
		"raw_foods", stats.rawFoods,
		"accepted", stats.accepted,
		"skipped_unparseable", stats.skippedUnparseable,
		"skipped_bad_macros", stats.skippedBadMacros,
		"returned", len(hits))
	return hits, nil
}

// getToken returns the cached OAuth2 access token, fetching a fresh one
// via the client-credentials grant when missing or near expiry.
func (p *FatSecretProvider) getToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Now().Before(p.tokenExpiresAt) {
		return p.token, nil
	}

	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {"basic"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(p.clientID, p.clientSecret)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fatsecret token: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("fatsecret token: decode response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("fatsecret token: response carried no access_token")
	}
	p.token = payload.AccessToken
	ttl := time.Duration(payload.ExpiresIn)*time.Second - tokenRefreshMargin
	if ttl < 0 {
		ttl = 0
	}
	p.tokenExpiresAt = time.Now().Add(ttl)
	p.log.DebugContext(ctx, "fatsecret oauth token refreshed",
		"expires_in_s", int(payload.ExpiresIn))
	return p.token, nil
}

type fatSecretFood struct {
	FoodID          json.Number `json:"food_id"`
	FoodName        string      `json:"food_name"`
	BrandName       string      `json:"brand_name"`
	FoodDescription string      `json:"food_description"`
}

type fatSecretParseStats struct {
	rawFoods           int
	accepted           int
	skippedUnparseable int
	skippedBadMacros   int
}

// parseFatSecretSearch extracts candidates from a foods.search JSON
// response. FatSecret's JSON is converted from XML: a single hit comes
// back as a bare object, multiple hits as an array — hence the
// json.RawMessage two-step.
func parseFatSecretSearch(ctx context.Context, log *slog.Logger, body []byte, limit int) ([]Candidate, fatSecretParseStats, error) {
	stats := fatSecretParseStats{}
	var payload struct {
		Foods struct {
			Food json.RawMessage `json:"food"`
		} `json:"foods"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, stats, fmt.Errorf("fatsecret search: decode response: %w", err)
	}
	raw := payload.Foods.Food
	if len(raw) == 0 || string(raw) == "null" {
		return nil, stats, nil
	}

	var foods []fatSecretFood
	if err := json.Unmarshal(raw, &foods); err != nil {
		var single fatSecretFood
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, stats, fmt.Errorf("fatsecret search: unexpected foods.food shape: %w", err)
		}
		foods = []fatSecretFood{single}
	}
	stats.rawFoods = len(foods)

	out := make([]Candidate, 0, len(foods))
	for _, food := range foods {
		m := fatSecretDescriptionRE.FindStringSubmatch(food.FoodDescription)
		if m == nil {
			// Skipped, never guessed: a candidate we can't parse macros
			// for is worse than one fewer candidate.
			stats.skippedUnparseable++
			log.DebugContext(ctx, "fatsecret candidate skipped: unparseable food_description",
				"food_id", food.FoodID.String(),
				"food_name", food.FoodName,
				"food_description", truncateLogField(food.FoodDescription, 120))
			continue
		}
		serving := m[fatSecretDescriptionRE.SubexpIndex("serving")]
		per, err := parseFatSecretMacros(m)
		if err != nil {
			stats.skippedBadMacros++
			log.DebugContext(ctx, "fatsecret candidate skipped: bad macro number",
				"food_id", food.FoodID.String(),
				"food_name", food.FoodName, "error", err)
			continue
		}
		candidate := newCandidate(
			food.FoodName, food.BrandName, serving,
			per, "fatsecret", food.FoodID.String(),
		)
		stats.accepted++
		log.DebugContext(ctx, "fatsecret candidate accepted",
			"food_id", food.FoodID.String(),
			"food_name", food.FoodName,
			"serving", serving,
			"calories", candidate.PerServing.Calories,
			"protein_g", candidate.PerServing.ProteinG,
			"fat_g", candidate.PerServing.FatG,
			"carbs_g", candidate.PerServing.CarbsG)
		out = append(out, candidate)
		if len(out) >= limit {
			break
		}
	}
	return out, stats, nil
}

// parseFatSecretMacros converts the regex's numeric captures. The
// pattern only admits digits and dots, but "1.2.3" still satisfies
// [\d.]+ — treat a parse failure like an unparseable description.
func parseFatSecretMacros(m []string) (Macros, error) {
	fields := map[string]*float64{}
	per := Macros{}
	fields["calories"] = &per.Calories
	fields["protein"] = &per.ProteinG
	fields["fat"] = &per.FatG
	fields["carbs"] = &per.CarbsG
	for name, dst := range fields {
		v, err := strconv.ParseFloat(m[fatSecretDescriptionRE.SubexpIndex(name)], 64)
		if err != nil {
			return Macros{}, err
		}
		*dst = v
	}
	return per, nil
}
