package weather

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
)

// Status is the explicit reading disposition — the tile never has to guess
// why a payload is thin.
type Status string

const (
	// StatusOK promises a complete, fresh payload: every section is present,
	// from fresh cache or fetched this request.
	StatusOK Status = "ok"
	// StatusStale means the payload is incomplete or partially old: at least
	// one section was served from an expired row, or was attempted and lost
	// entirely (failed fetch, no cache row).
	StatusStale Status = "stale"
	// StatusDisabled: feature flag off or provider unconfigured.
	StatusDisabled Status = "disabled"
	// StatusBudgetExhausted: the reservation was refused; the payload is
	// whatever cache existed (possibly nothing).
	StatusBudgetExhausted Status = "budget_exhausted"
	// StatusUnavailable: no current conditions at all — the tile is unusable,
	// though any forecast sections that did exist stay attached.
	StatusUnavailable Status = "unavailable"
)

// Reading is the assembled tile payload for one location, still metric;
// the handler converts units per user.
type Reading struct {
	Status    Status
	FetchedAt time.Time
	Current   *Current
	Hourly    []HourlyBucket
	Daily     *Daily
}

// Service orchestrates the cache-first flow modelled on nutritionlookup:
// fresh cache answers directly; anything non-fresh reserves budget BEFORE
// calling the provider; provider or budget failures degrade to stale cache
// where it exists rather than erroring. The Service works on coordinates —
// location resolution belongs to the handler.
type Service struct {
	cfg      config.WeatherConfig
	cache    CacheRepository
	budget   *BudgetLedger
	provider Provider
	log      *slog.Logger
	// now is injectable so tests can time-travel TTL boundaries.
	now func() time.Time
}

func NewService(cfg config.WeatherConfig, cache CacheRepository, budget *BudgetLedger, provider Provider, log *slog.Logger) *Service {
	return &Service{cfg: cfg, cache: cache, budget: budget, provider: provider, log: log, now: time.Now}
}

// cacheState classifies one cache read. "missing" covers absent rows,
// unreadable rows, and corrupt payloads alike — anything the service must
// treat as "no cached answer".
type cacheState int

const (
	cacheMissing cacheState = iota
	cacheStale
	cacheFresh
)

// endpointPlan carries one reading endpoint through classify → fetch →
// disposition. decode and fetch both land the value in the caller's result
// slot; fetch additionally returns the normalized JSON for the cache write.
type endpointPlan struct {
	endpoint Endpoint
	ttl      time.Duration
	decode   func(payload string) error
	fetch    func(ctx context.Context) (string, error)

	state     cacheState
	fetched   bool // provider answered this request
	fetchedAt time.Time
}

// served reports whether this endpoint contributes to the payload: fresh
// cache, a fresh fetch, or a stale row held as fallback.
func (p *endpointPlan) served() bool {
	return p.fetched || p.state != cacheMissing
}

func (p *endpointPlan) servedStale() bool {
	return !p.fetched && p.state == cacheStale
}

// Readings assembles the tile payload for one coordinate: cache-first per
// endpoint, one atomic budget reservation for everything non-fresh, then
// per-endpoint provider calls with stale fallback. Increments requestsTotal
// exactly once with the final outcome.
func (s *Service) Readings(ctx context.Context, lat, lon float64) Reading {
	if !s.cfg.Enabled || !s.provider.Configured() {
		requestsTotal.WithLabelValues("disabled").Inc()
		return Reading{Status: StatusDisabled}
	}

	var (
		current *Current
		hourly  []HourlyBucket
		daily   *Daily
	)
	// currentPlan is named because the disposition treats it specially: the
	// tile is unusable without current conditions.
	currentPlan := &endpointPlan{
		endpoint: EndpointCurrent,
		ttl:      time.Duration(s.cfg.CurrentTTLMinutes) * time.Minute,
		decode: func(payload string) error {
			var c Current
			if err := json.Unmarshal([]byte(payload), &c); err != nil {
				return err
			}
			current = &c
			return nil
		},
		fetch: func(ctx context.Context) (string, error) {
			c, err := s.provider.Current(ctx, lat, lon)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(c)
			if err != nil {
				return "", err
			}
			current = &c
			return string(b), nil
		},
	}
	plans := []*endpointPlan{
		currentPlan,
		{
			endpoint: EndpointHourly,
			ttl:      time.Duration(s.cfg.HourlyTTLMinutes) * time.Minute,
			decode: func(payload string) error {
				var h []HourlyBucket
				if err := json.Unmarshal([]byte(payload), &h); err != nil {
					return err
				}
				hourly = h
				return nil
			},
			fetch: func(ctx context.Context) (string, error) {
				h, err := s.provider.Hourly(ctx, lat, lon)
				if err != nil {
					return "", err
				}
				b, err := json.Marshal(h)
				if err != nil {
					return "", err
				}
				hourly = h
				return string(b), nil
			},
		},
		{
			endpoint: EndpointDaily,
			ttl:      time.Duration(s.cfg.DailyTTLMinutes) * time.Minute,
			decode: func(payload string) error {
				var d Daily
				if err := json.Unmarshal([]byte(payload), &d); err != nil {
					return err
				}
				daily = &d
				return nil
			},
			fetch: func(ctx context.Context) (string, error) {
				d, err := s.provider.Daily(ctx, lat, lon)
				if err != nil {
					return "", err
				}
				b, err := json.Marshal(d)
				if err != nil {
					return "", err
				}
				daily = &d
				return string(b), nil
			},
		},
	}

	// Classify every endpoint against its TTL. Stale rows decode immediately
	// so they are already in place as the fallback if the refetch fails; a
	// successful refetch simply overwrites the slot.
	var toFetch []*endpointPlan
	for _, p := range plans {
		p.state, p.fetchedAt = s.classify(ctx, ReadingKey(lat, lon, p.endpoint), p.ttl, p.decode)
		if p.state != cacheFresh {
			toFetch = append(toFetch, p)
		}
	}

	assemble := func(status Status) Reading {
		return Reading{
			Status:    status,
			FetchedAt: oldestServed(plans),
			Current:   current,
			Hourly:    hourly,
			Daily:     daily,
		}
	}

	if len(toFetch) == 0 {
		requestsTotal.WithLabelValues("cache_hit").Inc()
		s.log.DebugContext(ctx, "weather: readings cache hit", "lat", lat, "lon", lon)
		return assemble(StatusOK)
	}

	// Reserve everything non-fresh atomically BEFORE any provider call, so a
	// partial refresh can't consume budget it wasn't granted.
	switch err := s.budget.Reserve(ctx, len(toFetch), activeCeiling(s.cfg)); {
	case errors.Is(err, ErrBudgetExhausted):
		requestsTotal.WithLabelValues("budget_exhausted").Inc()
		s.log.WarnContext(ctx, "weather: budget exhausted; serving cache as-is",
			"lat", lat, "lon", lon, "wanted", len(toFetch))
		return assemble(StatusBudgetExhausted)
	case err != nil:
		// A broken ledger is treated like a provider failure: no calls are
		// made (the reservation didn't happen) and the request degrades to
		// whatever cache exists.
		s.log.ErrorContext(ctx, "weather: budget reserve failed",
			"lat", lat, "lon", lon, "error", err)
	default:
		now := s.now().UTC()
		for _, p := range toFetch {
			payload, err := s.timedFetch(ctx, p.endpoint, p.fetch)
			if err != nil {
				// Stale fallback (if any) is already decoded into the slot.
				s.log.WarnContext(ctx, "weather: provider call failed",
					"endpoint", p.endpoint, "lat", lat, "lon", lon, "error", err)
				continue
			}
			p.fetched = true
			p.fetchedAt = now
			s.putCache(ctx, ReadingKey(lat, lon, p.endpoint), payload, now)
		}
	}

	// Disposition. "No current at all" trumps staleness: the tile is
	// unusable without current conditions, though any forecast we do have
	// stays attached. With current in hand, ok/served promise every section
	// present and fresh — a section served from an expired row OR lost
	// entirely to a failed fetch degrades to stale/served_stale, so the
	// payload is never silently thin.
	switch {
	case !currentPlan.served():
		requestsTotal.WithLabelValues("failed").Inc()
		s.log.WarnContext(ctx, "weather: readings unavailable", "lat", lat, "lon", lon)
		return assemble(StatusUnavailable)
	case anyDegraded(plans):
		requestsTotal.WithLabelValues("served_stale").Inc()
		return assemble(StatusStale)
	default:
		requestsTotal.WithLabelValues("served").Inc()
		return assemble(StatusOK)
	}
}

// Search resolves a place-name query, cache-first on the normalized query
// key. Geocoding reserves budget only when cfg.CountGeocodingCalls is set.
// Errors degrade to (nil, StatusUnavailable, nil) — callers never see raw
// provider failures.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]GeoResult, Status, error) {
	return s.geocode(ctx, GeocodeDirectKey(query), EndpointGeocodeDirect, limit,
		func(ctx context.Context) ([]GeoResult, error) {
			return s.provider.GeocodeDirect(ctx, query, limit)
		})
}

// Reverse resolves coordinates to place candidates; same flow as Search on
// the reverse key. There is no caller-facing limit: the provider pins
// limit=1 upstream (reverse lookup exists to label a coordinate), so the
// geocode flow gets 0 = uncapped.
func (s *Service) Reverse(ctx context.Context, lat, lon float64) ([]GeoResult, Status, error) {
	return s.geocode(ctx, GeocodeReverseKey(lat, lon), EndpointGeocodeReverse, 0,
		func(ctx context.Context) ([]GeoResult, error) {
			return s.provider.GeocodeReverse(ctx, lat, lon)
		})
}

// truncated caps a geocode result set to limit; limit <= 0 means uncapped.
// Cache rows are deliberately limit-agnostic (shared across callers), so
// every serve path — fresh hit, stale fallback, fresh fetch — must apply the
// caller's limit on the way out.
func truncated(results []GeoResult, limit int) []GeoResult {
	if limit > 0 && len(results) > limit {
		return results[:limit]
	}
	return results
}

// geocode is the shared Search/Reverse flow: fresh cache serves directly, an
// expired row is refetched but kept as the stale fallback, budget refusal or
// provider failure degrades. Increments requestsTotal exactly once.
func (s *Service) geocode(ctx context.Context, key string, endpoint Endpoint, limit int, call func(ctx context.Context) ([]GeoResult, error)) ([]GeoResult, Status, error) {
	if !s.cfg.Enabled || !s.provider.Configured() {
		requestsTotal.WithLabelValues("disabled").Inc()
		return nil, StatusDisabled, nil
	}

	ttl := time.Duration(s.cfg.GeocodingTTLDays) * 24 * time.Hour
	var results []GeoResult
	state, _ := s.classify(ctx, key, ttl, func(payload string) error {
		var cached []GeoResult
		if err := json.Unmarshal([]byte(payload), &cached); err != nil {
			return err
		}
		results = cached
		return nil
	})
	if state == cacheFresh {
		requestsTotal.WithLabelValues("cache_hit").Inc()
		s.log.DebugContext(ctx, "weather: geocode cache hit", "endpoint", endpoint, "key", key)
		return truncated(results, limit), StatusOK, nil
	}
	// From here results holds the expired row (when staleAvailable) — the
	// fallback if the refetch cannot happen; a successful fetch overwrites it.
	staleAvailable := state == cacheStale
	serveStale := func() ([]GeoResult, Status, error) {
		requestsTotal.WithLabelValues("served_stale").Inc()
		s.log.WarnContext(ctx, "weather: serving expired geocode row",
			"endpoint", endpoint, "key", key)
		return truncated(results, limit), StatusStale, nil
	}

	if s.cfg.CountGeocodingCalls {
		switch err := s.budget.Reserve(ctx, 1, activeCeiling(s.cfg)); {
		case errors.Is(err, ErrBudgetExhausted):
			if staleAvailable {
				return serveStale()
			}
			requestsTotal.WithLabelValues("budget_exhausted").Inc()
			return nil, StatusBudgetExhausted, nil
		case err != nil:
			// Broken ledger degrades like a provider failure.
			s.log.ErrorContext(ctx, "weather: budget reserve failed",
				"endpoint", endpoint, "key", key, "error", err)
			if staleAvailable {
				return serveStale()
			}
			requestsTotal.WithLabelValues("failed").Inc()
			return nil, StatusUnavailable, nil
		}
	}

	// One clock read stamps the fetch and the cache row, same as Readings.
	now := s.now().UTC()
	payload, err := s.timedFetch(ctx, endpoint, func(ctx context.Context) (string, error) {
		fetched, err := call(ctx)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(fetched)
		if err != nil {
			return "", err
		}
		results = fetched
		return string(b), nil
	})
	if err != nil {
		s.log.WarnContext(ctx, "weather: geocode call failed",
			"endpoint", endpoint, "key", key, "error", err)
		if staleAvailable {
			return serveStale()
		}
		requestsTotal.WithLabelValues("failed").Inc()
		return nil, StatusUnavailable, nil
	}
	s.putCache(ctx, key, payload, now)
	requestsTotal.WithLabelValues("served").Inc()
	return truncated(results, limit), StatusOK, nil
}

// classify reads one cache row, records the cache event (hit / stale / miss
// / corrupt / read_error), and decodes the payload into the caller's slot
// for both fresh and stale rows. Read errors and corrupt payloads degrade to
// missing — the cache is an optimization, never a gate.
func (s *Service) classify(ctx context.Context, key string, ttl time.Duration, decode func(payload string) error) (cacheState, time.Time) {
	row, err := s.cache.Get(ctx, key)
	switch {
	case err != nil:
		cacheEventsTotal.WithLabelValues("read_error").Inc()
		s.log.WarnContext(ctx, "weather: cache read failed", "key", key, "error", err)
		return cacheMissing, time.Time{}
	case row == nil:
		cacheEventsTotal.WithLabelValues("miss").Inc()
		return cacheMissing, time.Time{}
	}
	if err := decode(row.PayloadJSON); err != nil {
		cacheEventsTotal.WithLabelValues("corrupt").Inc()
		s.log.WarnContext(ctx, "weather: corrupt cache row; refetching", "key", key, "error", err)
		return cacheMissing, time.Time{}
	}
	if s.now().UTC().Sub(row.FetchedAt) < ttl {
		cacheEventsTotal.WithLabelValues("hit").Inc()
		return cacheFresh, row.FetchedAt
	}
	cacheEventsTotal.WithLabelValues("stale").Inc()
	return cacheStale, row.FetchedAt
}

// timedFetch wraps one provider call with the latency histogram and the
// per-endpoint ok/error counter.
func (s *Service) timedFetch(ctx context.Context, endpoint Endpoint, fetch func(ctx context.Context) (string, error)) (string, error) {
	started := s.now()
	payload, err := fetch(ctx)
	providerLatency.WithLabelValues(string(endpoint)).Observe(s.now().Sub(started).Seconds())
	if err != nil {
		providerCallsTotal.WithLabelValues(string(endpoint), "error").Inc()
		return "", err
	}
	providerCallsTotal.WithLabelValues(string(endpoint), "ok").Inc()
	return payload, nil
}

// putCache upserts one normalized payload. Write errors are logged and
// swallowed — the cache is an optimization — but counted, because nothing
// else surfaces a dying cache.
func (s *Service) putCache(ctx context.Context, key, payload string, fetchedAt time.Time) {
	if err := s.cache.Put(ctx, CacheRow{
		CacheKey:    key,
		PayloadJSON: payload,
		FetchedAt:   fetchedAt,
		LastUsedAt:  fetchedAt,
	}); err != nil {
		cacheWritesTotal.WithLabelValues("error").Inc()
		s.log.WarnContext(ctx, "weather: cache write failed", "key", key, "error", err)
		return
	}
	cacheWritesTotal.WithLabelValues("ok").Inc()
}

// oldestServed is the honest "updated Xh ago": the oldest fetched_at among
// the endpoints that actually contributed to the payload. Zero when nothing
// was served.
func oldestServed(plans []*endpointPlan) time.Time {
	var oldest time.Time
	for _, p := range plans {
		if !p.served() {
			continue
		}
		if oldest.IsZero() || p.fetchedAt.Before(oldest) {
			oldest = p.fetchedAt
		}
	}
	return oldest
}

// anyDegraded reports whether some endpoint makes the payload less than the
// StatusOK promise of "every section present and fresh": served from an
// expired row, or attempted and ended with nothing (failed fetch, no cache
// row) — a silently missing section is degradation just like a stale one.
func anyDegraded(plans []*endpointPlan) bool {
	for _, p := range plans {
		if p.servedStale() || !p.served() {
			return true
		}
	}
	return false
}
