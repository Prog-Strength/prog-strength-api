package nutritionlookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// freshnessTTL is how long a cache row serves directly before a
// provider re-pull is attempted. Code-pinned, not env — same philosophy
// as auth.JWTLifetime: changing data-freshness semantics should be a
// reviewable code change, not a quiet ops tweak. 7 days balances "fast
// repeat lookups, quota protection" against "reformulated foods don't
// serve stale macros forever."
const freshnessTTL = 7 * 24 * time.Hour

// ErrUnavailable is returned when no nutrition data provider has
// credentials configured. The handler maps it to 503
// lookup_unavailable; the agent falls back to estimating.
var ErrUnavailable = errors.New("no nutrition data providers configured")

// ErrFailed is returned (wrapped, with per-provider detail) when every
// configured provider errored and no cache row — fresh or stale —
// could answer. The handler maps it to 503 lookup_failed.
var ErrFailed = errors.New("nutrition lookup failed")

// Result is the lookup payload: scaled candidates plus the quantity
// they were scaled by. Matches may be empty — "the providers are up
// but found nothing" is a 200, not an error.
type Result struct {
	Matches  []Candidate `json:"matches"`
	Quantity float64     `json:"quantity"`
}

// Service orchestrates cache-first lookup → provider merge (FatSecret
// first, USDA appended while short of maxResults) → quantity scaling →
// plausibility flags. Provider errors degrade: one provider down →
// results from the other; all down with a stale cache row → serve
// stale, flagged; all down with no cache → ErrFailed.
type Service struct {
	repo      Repository
	providers []Provider
	// now is injectable so tests can time-travel the freshness check.
	now func() time.Time
}

func NewService(repo Repository, providers ...Provider) *Service {
	return &Service{repo: repo, providers: providers, now: time.Now}
}

// Lookup resolves query into up to maxResults candidates scaled to
// quantity. The query is normalized (lower-cased, whitespace-collapsed)
// so "Chicken  Minis" and "chicken minis" share one cache row.
func (s *Service) Lookup(ctx context.Context, query string, quantity float64, maxResults int) (Result, error) {
	normalized := normalizeQuery(query)

	// Cache first: a row fresher than freshnessTTL answers without any
	// external call. A row past the TTL is kept around as the stale
	// fallback in case every provider is down.
	var staleRow *CacheRow
	row, err := s.repo.Get(ctx, normalized)
	if err != nil {
		// A broken cache read degrades to a provider pull — the cache
		// is an optimization, never a gate.
		log.Printf("nutritionlookup: cache get %q failed: %v", normalized, err)
	} else if row != nil {
		candidates, err := unmarshalCandidates(row.CandidatesJSON)
		switch {
		case err != nil:
			log.Printf("nutritionlookup: corrupt cache row %q, re-pulling: %v", normalized, err)
		case s.now().UTC().Sub(row.FetchedAt) < freshnessTTL:
			return s.result(candidates, quantity, maxResults, false), nil
		default:
			staleRow = row
		}
	}

	// Stale or missing: pull from providers in order — FatSecret first
	// (restaurant + branded coverage), USDA appended while short of
	// maxResults. Unconfigured providers are skipped; erroring ones are
	// logged and collected so the failure detail names each source.
	var (
		merged        []Candidate
		providerErrs  []string
		anyConfigured bool
	)
	for _, p := range s.providers {
		if !p.Configured() {
			continue
		}
		anyConfigured = true
		if len(merged) >= maxResults {
			break
		}
		hits, err := p.Search(ctx, normalized, maxResults-len(merged))
		if err != nil {
			log.Printf("nutritionlookup: lookup via %s failed: %v", p.Source(), err)
			providerErrs = append(providerErrs, fmt.Sprintf("%s: %v", p.Source(), err))
			continue
		}
		merged = append(merged, hits...)
	}

	if !anyConfigured {
		return Result{}, ErrUnavailable
	}

	if len(merged) == 0 && len(providerErrs) > 0 {
		// Every provider that could have answered failed. A stale row
		// beats no answer — serve it flagged so the agent can mention
		// the data may be out of date. Resilience over purity.
		if staleRow != nil {
			candidates, err := unmarshalCandidates(staleRow.CandidatesJSON)
			if err == nil {
				return s.result(candidates, quantity, maxResults, true), nil
			}
		}
		return Result{}, fmt.Errorf("%w: %s", ErrFailed, strings.Join(providerErrs, "; "))
	}

	// Only cache real results — a transient empty answer shouldn't pin
	// "no matches" for a whole freshness window.
	if len(merged) > 0 {
		candidatesJSON, err := json.Marshal(merged)
		if err != nil {
			log.Printf("nutritionlookup: marshal candidates for %q failed: %v", normalized, err)
		} else {
			now := s.now().UTC()
			if err := s.repo.Put(ctx, CacheRow{
				QueryNormalized: normalized,
				CandidatesJSON:  string(candidatesJSON),
				FetchedAt:       now,
				LastUsedAt:      now,
			}); err != nil {
				// A broken cache write doesn't break the lookup.
				log.Printf("nutritionlookup: cache put %q failed: %v", normalized, err)
			}
		}
	}

	return s.result(merged, quantity, maxResults, false), nil
}

// result scales per-serving candidates to quantity (attaching
// plausibility warnings) and truncates to maxResults. stale marks every
// candidate as served from an expired cache row.
func (s *Service) result(candidates []Candidate, quantity float64, maxResults int, stale bool) Result {
	if len(candidates) > maxResults {
		candidates = candidates[:maxResults]
	}
	matches := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		out := scaled(c, quantity)
		out.Stale = stale
		matches = append(matches, out)
	}
	return Result{Matches: matches, Quantity: quantity}
}

// normalizeQuery lower-cases and collapses all whitespace runs to
// single spaces — the global cache key shared across users.
func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func unmarshalCandidates(candidatesJSON string) ([]Candidate, error) {
	var out []Candidate
	if err := json.Unmarshal([]byte(candidatesJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}
