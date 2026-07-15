package nutritionlookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
//
// Every decision point logs through the injected request-id-aware
// logger (see logging.go): cache hit/miss/stale, each provider call's
// outcome and latency, cache write success/failure, and the degraded
// paths — so one CloudWatch `filter request_id = "…"` reconstructs
// exactly what the backend did for a request.
type Service struct {
	repo      Repository
	providers []Provider
	log       *slog.Logger
	// now is injectable so tests can time-travel the freshness check.
	now func() time.Time
}

func NewService(repo Repository, log *slog.Logger, providers ...Provider) *Service {
	return &Service{repo: repo, providers: providers, log: log, now: time.Now}
}

// Lookup resolves query into up to maxResults candidates scaled to
// quantity. The query is normalized (lower-cased, whitespace-collapsed)
// so "Chicken  Minis" and "chicken minis" share one cache row.
func (s *Service) Lookup(ctx context.Context, query string, quantity float64, maxResults int) (Result, error) {
	normalized := normalizeQuery(query)
	s.log.DebugContext(ctx, "lookup start",
		"query", normalized, "quantity", quantity, "max_results", maxResults)

	// Cache first: a row fresher than freshnessTTL answers without any
	// external call. A row past the TTL is kept around as the stale
	// fallback in case every provider is down.
	var staleRow *CacheRow
	row, err := s.repo.Get(ctx, normalized)
	switch {
	case err != nil:
		// A broken cache read degrades to a provider pull — the cache
		// is an optimization, never a gate.
		cacheEventsTotal.WithLabelValues("read_error").Inc()
		s.log.WarnContext(ctx, "cache read failed; falling through to providers",
			"query", normalized, "error", err)
	case row == nil:
		cacheEventsTotal.WithLabelValues("miss").Inc()
		s.log.DebugContext(ctx, "cache miss", "query", normalized)
	default:
		age := s.now().UTC().Sub(row.FetchedAt)
		candidates, err := unmarshalCandidates(row.CandidatesJSON)
		switch {
		case err != nil:
			cacheEventsTotal.WithLabelValues("corrupt").Inc()
			s.log.WarnContext(ctx, "corrupt cache row; re-pulling",
				"query", normalized, "error", err)
		case age < freshnessTTL:
			cacheEventsTotal.WithLabelValues("hit").Inc()
			lookupRequestsTotal.WithLabelValues("cache_hit").Inc()
			result := s.result(candidates, quantity, maxResults, false)
			s.log.InfoContext(ctx, "cache hit",
				"query", normalized,
				"age_hours", int(age.Hours()),
				"candidates", len(candidates),
				"disposition", "cache_hit",
				"macro_selection", macroSelectionForMatches(result.Matches))
			logLookupCandidates(ctx, s.log, "cache hit candidates", result.Matches,
				"query", normalized)
			return result, nil
		default:
			cacheEventsTotal.WithLabelValues("stale").Inc()
			s.log.DebugContext(ctx, "cache stale; re-pulling",
				"query", normalized, "age_hours", int(age.Hours()))
			staleRow = row
		}
	}

	// Stale or missing: pull from providers in order — FatSecret first
	// (restaurant + branded coverage), USDA appended while short of
	// maxResults. Unconfigured providers are skipped; erroring ones are
	// logged and collected so the failure detail names each source.
	var (
		merged           []Candidate
		providerErrs     []string
		anyConfigured    bool
		providersQueried []string
		fatsecretHits    int
		usdaHits         int
	)
	for _, p := range s.providers {
		if !p.Configured() {
			s.log.DebugContext(ctx, "provider skipped: not configured", "source", p.Source())
			continue
		}
		anyConfigured = true
		if len(merged) >= maxResults {
			s.log.DebugContext(ctx, "provider skipped: quota filled",
				"source", p.Source(), "merged", len(merged), "max_results", maxResults)
			break
		}
		requested := maxResults - len(merged)
		started := s.now()
		hits, err := p.Search(ctx, normalized, requested)
		elapsed := s.now().Sub(started)
		providerDuration.WithLabelValues(p.Source()).Observe(elapsed.Seconds())
		if err != nil {
			providerRequestsTotal.WithLabelValues(p.Source(), "error").Inc()
			s.log.WarnContext(ctx, "provider search failed",
				"source", p.Source(), "query", normalized,
				"requested", requested,
				"elapsed_ms", elapsed.Milliseconds(), "error", err)
			providerErrs = append(providerErrs, fmt.Sprintf("%s: %v", p.Source(), err))
			continue
		}
		providerRequestsTotal.WithLabelValues(p.Source(), "ok").Inc()
		providersQueried = append(providersQueried, p.Source())
		switch p.Source() {
		case "fatsecret":
			fatsecretHits = len(hits)
		case "usda":
			usdaHits = len(hits)
		}
		s.log.InfoContext(ctx, "provider search ok",
			"source", p.Source(), "query", normalized,
			"requested", requested, "hits", len(hits),
			"elapsed_ms", elapsed.Milliseconds())
		merged = append(merged, hits...)
	}
	if len(providersQueried) > 0 {
		s.log.InfoContext(ctx, "lookup provider merge",
			"query", normalized,
			"max_results", maxResults,
			"providers_queried", strings.Join(providersQueried, ","),
			"fatsecret_hits", fatsecretHits,
			"usda_hits", usdaHits,
			"merged", len(merged),
			"merge_order", "fatsecret_first_then_usda_while_short_of_max_results",
			"macro_selection", "api_returns_candidates_agent_chooses")
	}

	if !anyConfigured {
		lookupRequestsTotal.WithLabelValues("unavailable").Inc()
		s.log.InfoContext(ctx, "lookup unavailable: no providers configured",
			"query", normalized,
			"macro_selection", macroSelectionAgentMustEstimate)
		return Result{}, ErrUnavailable
	}

	if len(merged) == 0 && len(providerErrs) > 0 {
		// Every provider that could have answered failed. A stale row
		// beats no answer — serve it flagged so the agent can mention
		// the data may be out of date. Resilience over purity.
		if staleRow != nil {
			candidates, err := unmarshalCandidates(staleRow.CandidatesJSON)
			if err == nil {
				lookupRequestsTotal.WithLabelValues("served_stale").Inc()
				result := s.result(candidates, quantity, maxResults, true)
				s.log.WarnContext(ctx, "all providers failed; serving stale cache",
					"query", normalized,
					"age_hours", int(s.now().UTC().Sub(staleRow.FetchedAt).Hours()),
					"candidates", len(candidates),
					"disposition", "served_stale",
					"macro_selection", macroSelectionForMatches(result.Matches))
				logLookupCandidates(ctx, s.log, "stale cache candidates", result.Matches,
					"query", normalized)
				return result, nil
			}
		}
		lookupRequestsTotal.WithLabelValues("failed").Inc()
		s.log.WarnContext(ctx, "lookup failed: all providers errored, no usable cache",
			"query", normalized, "errors", strings.Join(providerErrs, "; "),
			"macro_selection", macroSelectionAgentMustEstimate)
		return Result{}, fmt.Errorf("%w: %s", ErrFailed, strings.Join(providerErrs, "; "))
	}

	// Only cache real results — a transient empty answer shouldn't pin
	// "no matches" for a whole freshness window.
	if len(merged) > 0 {
		candidatesJSON, err := json.Marshal(merged)
		if err != nil {
			s.log.WarnContext(ctx, "cache write skipped: marshal failed",
				"query", normalized, "error", err)
		} else {
			now := s.now().UTC()
			if err := s.repo.Put(ctx, CacheRow{
				QueryNormalized: normalized,
				CandidatesJSON:  string(candidatesJSON),
				FetchedAt:       now,
				LastUsedAt:      now,
			}); err != nil {
				// A broken cache write doesn't break the lookup.
				cacheWritesTotal.WithLabelValues("error").Inc()
				s.log.WarnContext(ctx, "cache write failed",
					"query", normalized, "error", err)
			} else {
				cacheWritesTotal.WithLabelValues("ok").Inc()
				s.log.DebugContext(ctx, "cache write ok",
					"query", normalized, "candidates", len(merged))
			}
		}
	}

	lookupRequestsTotal.WithLabelValues("served").Inc()
	result := s.result(merged, quantity, maxResults, false)
	disposition := "served_fresh"
	if len(result.Matches) == 0 {
		disposition = "served_empty"
	}
	s.log.InfoContext(ctx, "lookup served",
		"query", normalized,
		"disposition", disposition,
		"matches", len(result.Matches),
		"macro_selection", macroSelectionForMatches(result.Matches))
	logLookupCandidates(ctx, s.log, "lookup candidates", result.Matches,
		"query", normalized)
	return result, nil
}

func macroSelectionForMatches(matches []Candidate) string {
	if len(matches) == 0 {
		return macroSelectionAgentMustEstimate
	}
	return macroSelectionAgentChooses
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
