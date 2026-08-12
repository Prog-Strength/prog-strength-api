package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// userReader is the slice of the user store the handler needs: the
// distance_unit preference that picks °F/mph vs °C/km/h. Narrow on purpose —
// the handler must not grow a dependency on the full user.Repository surface.
type userReader interface {
	GetByID(ctx context.Context, id string) (*user.User, error)
}

// The full user repository satisfies the seam.
var _ userReader = user.Repository(nil)

// Handler exposes the five /weather routes. The service works in metric on
// raw coordinates; everything user-facing — location resolution, unit
// conversion, saved-list management — lives here.
type Handler struct {
	svc       *Service
	locations LocationsRepository
	cfg       config.WeatherConfig
	users     userReader
	log       *slog.Logger
	// now is injectable so tests can pin the hourly "future buckets" filter.
	now func() time.Time
}

func NewHandler(svc *Service, locations LocationsRepository, cfg config.WeatherConfig, users userReader, log *slog.Logger) *Handler {
	return &Handler{svc: svc, locations: locations, cfg: cfg, users: users, log: log, now: time.Now}
}

// Mount registers routes under the given router. Callers are expected to have
// already wrapped the router in auth.RequireUser — every route reads the user
// ID from context. The routes are registered unconditionally: the kill switch
// changes payloads (status "disabled"), never the route table, so a disabled
// feature stays distinguishable from a bad deploy (which 404s).
func (h *Handler) Mount(r chi.Router) {
	r.Get("/weather", h.readings)
	r.Get("/weather/locations", h.listLocations)
	r.Put("/weather/locations", h.putLocations)
	r.Get("/weather/search", h.search)
	r.Get("/weather/reverse", h.reverse)
}

// ---- response shapes -------------------------------------------------------

type locationRefPayload struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	State   *string `json:"state,omitempty"`
	Country string  `json:"country"`
}

type unitsPayload struct {
	Temp string `json:"temp"` // "F" | "C"
	Wind string `json:"wind"` // "mph" | "km/h"
}

type currentPayload struct {
	Temp      int    `json:"temp"`
	FeelsLike int    `json:"feels_like"`
	Humidity  int    `json:"humidity"`
	WindSpeed int    `json:"wind_speed"`
	Condition string `json:"condition"`
	Icon      string `json:"icon"`
}

type todayPayload struct {
	High    int       `json:"high"`
	Low     int       `json:"low"`
	Sunrise time.Time `json:"sunrise"`
	Sunset  time.Time `json:"sunset"`
}

type hourlyPayload struct {
	At   time.Time `json:"at"`
	Temp int       `json:"temp"`
	Icon string    `json:"icon"`
}

// dailyPayload is one day of the week forecast. PrecipChance is a PERCENT
// here — the model carries the provider's 0..1 probability, and the wire
// carries what a person reads — so a client never multiplies by 100 itself and
// no client can forget to.
type dailyPayload struct {
	At           time.Time `json:"at"`
	High         int       `json:"high"`
	Low          int       `json:"low"`
	Condition    string    `json:"condition"`
	Icon         string    `json:"icon"`
	PrecipChance int       `json:"precip_chance"`
	WindSpeed    int       `json:"wind_speed"`
	Humidity     int       `json:"humidity"`
	Sunrise      time.Time `json:"sunrise"`
	Sunset       time.Time `json:"sunset"`
}

// readingsResponse is the GET /weather data section. Timestamps stay RFC3339
// UTC — the client sent its IANA zone only for validation symmetry with
// /dashboard/summary and does its own local formatting. Every field except
// status is omitempty so the disabled shape collapses to {"status":"disabled"}.
type readingsResponse struct {
	Status    Status              `json:"status"`
	Location  *locationRefPayload `json:"location,omitempty"`
	FetchedAt string              `json:"fetched_at,omitempty"`
	Units     *unitsPayload       `json:"units,omitempty"`
	Current   *currentPayload     `json:"current,omitempty"`
	Today     *todayPayload       `json:"today,omitempty"`
	// Hourly is every FUTURE bucket the one hourly call returned, not the five
	// the tile strip shows. The client slices what it wants — the day view
	// wants all of them, and re-slicing costs nothing where a second call
	// would cost a provider credit.
	Hourly []hourlyPayload `json:"hourly,omitempty"`
	Daily  []dailyPayload  `json:"daily,omitempty"`
}

// locationItem is one saved location on the wire, shared by the GET list, the
// PUT request body, and the PUT echo. Coordinates ARE included in reads: the
// client PUTs the whole list back (replace, not patch), so omitting lat/lon
// would make an innocent reorder destroy every location's coordinates.
type locationItem struct {
	ID      string  `json:"id,omitempty"`
	Label   string  `json:"label"`
	State   *string `json:"state,omitempty"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

type settingsPayload struct {
	Enabled               bool `json:"enabled"`
	MaxLocations          int  `json:"max_locations"`
	EagerLoadAllLocations bool `json:"eager_load_all_locations"`
}

type geoResponse struct {
	Results []GeoResult `json:"results"`
	Status  Status      `json:"status"`
}

// ---- GET /weather ----------------------------------------------------------

func (h *Handler) readings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	// timezone is validated exactly like /dashboard/summary. The payload's
	// timestamps stay UTC; requiring the zone keeps the request contract
	// uniform across dashboard endpoints and catches misconfigured clients.
	tz := r.URL.Query().Get("timezone")
	if tz == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	if _, err := time.LoadLocation(tz); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid timezone "+tz)
		return
	}

	// Validated at the boundary, ahead of the kill switch: the value becomes a
	// metric label, so a caller sending a bad one should hear about it whether
	// or not the feature happens to be on today.
	src, srcOK := ParseSource(r.URL.Query().Get("source"))
	if !srcOK {
		httpresp.Error(w, http.StatusBadRequest, `source must be "tile" or "agent"`)
		return
	}

	// Disabled is checked BEFORE location resolution: with the feature off,
	// "you have no saved locations" would be noise — the only fact that
	// matters is that the tile is off.
	if !h.cfg.Enabled {
		httpresp.OK(w, "weather readings", readingsResponse{Status: StatusDisabled})
		return
	}

	locs, err := h.locations.List(ctx, userID)
	if err != nil {
		httpresp.ServerError(w, ctx, "list weather locations", err)
		return
	}
	var loc *Location
	if wantID := r.URL.Query().Get("location_id"); wantID != "" {
		for i := range locs {
			if locs[i].ID == wantID {
				loc = &locs[i]
				break
			}
		}
		if loc == nil {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "location not found", "location_not_found")
			return
		}
	} else {
		if len(locs) == 0 {
			httpresp.ErrorWithCode(w, http.StatusNotFound, "no saved locations", "no_locations")
			return
		}
		loc = &locs[0]
	}

	reading := h.svc.Readings(ctx, loc.Lat, loc.Lon, src)
	if reading.Status == StatusDisabled {
		// Provider unconfigured (no API key) — same disabled shape as the
		// cfg kill switch above.
		httpresp.OK(w, "weather readings", readingsResponse{Status: StatusDisabled})
		return
	}

	unit := h.distanceUnit(ctx, userID)
	httpresp.OK(w, "weather readings", buildReadings(reading, *loc, unit, h.now().UTC()))
}

// distanceUnit reads the user's unit preference. A failed read is logged and
// falls back to "mi" — the users-table default — rather than 500ing: failing
// the whole weather request over a display-preference read would be strictly
// worse than a possibly-wrong unit on one response.
func (h *Handler) distanceUnit(ctx context.Context, userID string) user.DistanceUnit {
	u, err := h.users.GetByID(ctx, userID)
	if err != nil {
		h.log.WarnContext(ctx, "weather: user lookup for unit preference failed; defaulting to mi",
			"user_id", userID, "error", err)
		return user.DistanceUnitMiles
	}
	return u.DistanceUnit
}

// buildReadings converts the service's metric reading into the response for
// the given unit preference. Unit conversion is handler-owned: the service
// and cache stay metric so two users with different preferences share rows.
func buildReadings(reading Reading, loc Location, unit user.DistanceUnit, now time.Time) readingsResponse {
	resp := readingsResponse{
		Status: reading.Status,
		Location: &locationRefPayload{
			ID:      loc.ID,
			Label:   loc.Label,
			State:   loc.State,
			Country: loc.Country,
		},
		Units: unitsFor(unit),
	}
	if !reading.FetchedAt.IsZero() {
		resp.FetchedAt = reading.FetchedAt.UTC().Format(time.RFC3339)
	}
	if c := reading.Current; c != nil {
		resp.Current = &currentPayload{
			Temp:      convTemp(c.TempC, unit),
			FeelsLike: convTemp(c.FeelsLikeC, unit),
			Humidity:  c.Humidity,
			WindSpeed: convWind(c.WindKMH, unit),
			Condition: c.Condition,
			Icon:      c.Icon,
		}
	}
	if d := reading.Daily; d != nil {
		resp.Today = &todayPayload{
			High:    convTemp(d.HighC, unit),
			Low:     convTemp(d.LowC, unit),
			Sunrise: d.Sunrise.UTC(),
			Sunset:  d.Sunset.UTC(),
		}
		for _, day := range d.Days {
			resp.Daily = append(resp.Daily, dailyPayload{
				At:        day.At.UTC(),
				High:      convTemp(day.HighC, unit),
				Low:       convTemp(day.LowC, unit),
				Condition: day.Condition,
				Icon:      day.Icon,
				// 0..1 probability → the percentage a person reads.
				PrecipChance: int(math.Round(day.PrecipChance * 100)),
				WindSpeed:    convWind(day.WindKMH, unit),
				Humidity:     day.Humidity,
				Sunrise:      day.Sunrise.UTC(),
				Sunset:       day.Sunset.UTC(),
			})
		}
	}
	// The strip shows what's coming, not what already happened: keep only
	// buckets at >= now. Assumes provider-ascending bucket order (true for
	// OpenWeather, preserved by the cache round-trip).
	//
	// Uncapped on purpose. It used to stop at 5 — the tile strip's width —
	// which meant the day view could not exist without a second billed call
	// for hours the response already contained. The tile still shows 5; the
	// client decides.
	for _, b := range reading.Hourly {
		if b.At.Before(now) {
			continue
		}
		resp.Hourly = append(resp.Hourly, hourlyPayload{
			At:   b.At.UTC(),
			Temp: convTemp(b.TempC, unit),
			Icon: b.Icon,
		})
	}
	return resp
}

func unitsFor(unit user.DistanceUnit) *unitsPayload {
	if unit == user.DistanceUnitKilometers {
		return &unitsPayload{Temp: "C", Wind: "km/h"}
	}
	return &unitsPayload{Temp: "F", Wind: "mph"}
}

// convTemp renders a Celsius temperature in the user's unit as a rounded int.
func convTemp(c float64, unit user.DistanceUnit) int {
	if unit == user.DistanceUnitKilometers {
		return int(math.Round(c))
	}
	return int(math.Round(c*9/5 + 32))
}

// convWind renders a km/h wind speed in the user's unit as a rounded int.
func convWind(kmh float64, unit user.DistanceUnit) int {
	if unit == user.DistanceUnitKilometers {
		return int(math.Round(kmh))
	}
	return int(math.Round(kmh / 1.609344))
}

// ---- GET /weather/locations ------------------------------------------------

// listLocations returns the saved list plus the settings block the client
// needs to render the manager (cap, eager-load, enabled). The list is served
// even when the feature is disabled — managing locations while the tile is
// off is harmless and lets the popover keep working; settings.enabled=false
// is the client's signal to grey out the tile itself.
func (h *Handler) listLocations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	locs, err := h.locations.List(ctx, userID)
	if err != nil {
		httpresp.ServerError(w, ctx, "list weather locations", err)
		return
	}
	httpresp.OK(w, "weather locations", map[string]any{
		"locations": locationItems(locs),
		"settings": settingsPayload{
			Enabled:               h.cfg.Enabled,
			MaxLocations:          h.cfg.MaxLocations,
			EagerLoadAllLocations: h.cfg.EagerLoadAllLocations,
		},
	})
}

func locationItems(locs []Location) []locationItem {
	items := make([]locationItem, 0, len(locs))
	for _, l := range locs {
		items = append(items, locationItem{
			ID:      l.ID,
			Label:   l.Label,
			State:   l.State,
			Country: l.Country,
			Lat:     l.Lat,
			Lon:     l.Lon,
		})
	}
	return items
}

// ---- PUT /weather/locations ------------------------------------------------

// Field caps for a saved location. Label matches what a human would type in
// the popover; state/country are short region codes or names from the
// geocoder (OpenWeather returns ISO country codes and short state strings).
const (
	maxLocationLabelLen  = 100
	maxLocationRegionLen = 10
)

// putLocations replaces the user's whole saved list (replace, not patch —
// same contract as the dashboard layout). Provided ids are preserved so
// existing rows keep their identity across reorders; rows without an id are
// new and get fresh ids in the repository. The write deliberately works even
// when the feature is disabled: persisting locations spends no provider
// budget, and keeping the popover functional through a kill-switch window is
// strictly better than stranding the user's edits.
func (h *Handler) putLocations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req struct {
		Locations []locationItem `json:"locations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Locations) > h.cfg.MaxLocations {
		httpresp.Error(w, http.StatusBadRequest,
			fmt.Sprintf("too many locations (max %d)", h.cfg.MaxLocations))
		return
	}

	locs := make([]Location, 0, len(req.Locations))
	seenID := make(map[string]bool, len(req.Locations))
	for _, item := range req.Locations {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			httpresp.Error(w, http.StatusBadRequest, "location label is required")
			return
		}
		if utf8.RuneCountInString(label) > maxLocationLabelLen {
			httpresp.Error(w, http.StatusBadRequest,
				fmt.Sprintf("location label is too long (max %d characters)", maxLocationLabelLen))
			return
		}
		country := strings.TrimSpace(item.Country)
		if country == "" {
			httpresp.Error(w, http.StatusBadRequest, "location country is required")
			return
		}
		if utf8.RuneCountInString(country) > maxLocationRegionLen {
			httpresp.Error(w, http.StatusBadRequest,
				fmt.Sprintf("location country is too long (max %d characters)", maxLocationRegionLen))
			return
		}
		// An empty/blank state normalizes to NULL so a hand-edited row reads
		// back the same as a geocode-sourced one (state omitted, not "").
		var state *string
		if item.State != nil {
			if s := strings.TrimSpace(*item.State); s != "" {
				if utf8.RuneCountInString(s) > maxLocationRegionLen {
					httpresp.Error(w, http.StatusBadRequest,
						fmt.Sprintf("location state is too long (max %d characters)", maxLocationRegionLen))
					return
				}
				state = &s
			}
		}
		if !validCoords(item.Lat, item.Lon) {
			httpresp.Error(w, http.StatusBadRequest, "invalid coordinates")
			return
		}
		if item.ID != "" {
			// A repeated id within one request would collide on the global
			// primary key mid-transaction; catch it here as a client error.
			if seenID[item.ID] {
				httpresp.Error(w, http.StatusBadRequest, "invalid location id")
				return
			}
			seenID[item.ID] = true
		}
		locs = append(locs, Location{
			ID:      item.ID,
			Label:   label,
			State:   state,
			Country: country,
			Lat:     item.Lat,
			Lon:     item.Lon,
		})
	}

	// Client-supplied ids must belong to the caller's own rows: the id column
	// is a GLOBAL primary key, so an id from another user's row would blow up
	// as a PK conflict (the delete only clears the caller's rows) — a
	// plausible client bug that should be a 400, not a 500 alert.
	if len(seenID) > 0 {
		existing, err := h.locations.List(ctx, userID)
		if err != nil {
			httpresp.ServerError(w, ctx, "list weather locations", err)
			return
		}
		owned := make(map[string]bool, len(existing))
		for _, l := range existing {
			owned[l.ID] = true
		}
		for id := range seenID {
			if !owned[id] {
				httpresp.Error(w, http.StatusBadRequest, "invalid location id")
				return
			}
		}
	}

	if err := h.locations.ReplaceAll(ctx, userID, locs); err != nil {
		httpresp.ServerError(w, ctx, "replace weather locations", err)
		return
	}
	// Echo the list as saved (re-read, so ids minted for new rows and the
	// stored ordering are what the client holds going forward).
	saved, err := h.locations.List(ctx, userID)
	if err != nil {
		httpresp.ServerError(w, ctx, "list weather locations", err)
		return
	}
	httpresp.OK(w, "weather locations saved", map[string]any{
		"locations": locationItems(saved),
	})
}

// validCoords rejects NaN/Inf and out-of-range latitude/longitude.
func validCoords(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// ---- GET /weather/search ---------------------------------------------------

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := auth.UserIDFrom(ctx); !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		httpresp.Error(w, http.StatusBadRequest, "q is required")
		return
	}
	if len(q) > 200 {
		httpresp.Error(w, http.StatusBadRequest, "q is too long (max 200 characters)")
		return
	}
	limit := 5
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 5 {
			httpresp.Error(w, http.StatusBadRequest, "limit must be an integer between 1 and 5")
			return
		}
		limit = v
	}

	// /weather/search and /weather/reverse are the dashboard popover's
	// location picker; there is no agent tool for either, so they are
	// attributed to the tile rather than growing a ?source= of their own.
	results, status, err := h.svc.Search(ctx, q, limit, SourceTile)
	if err != nil {
		httpresp.ServerError(w, ctx, "weather search", err)
		return
	}
	httpresp.OK(w, "weather search results", geoPayload(results, status))
}

// ---- GET /weather/reverse --------------------------------------------------

func (h *Handler) reverse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := auth.UserIDFrom(ctx); !ok {
		httpresp.ServerError(w, ctx, "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	lat, latErr := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, lonErr := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if latErr != nil || lonErr != nil || !validCoords(lat, lon) {
		httpresp.Error(w, http.StatusBadRequest, "invalid coordinates")
		return
	}

	results, status, err := h.svc.Reverse(ctx, lat, lon, SourceTile)
	if err != nil {
		httpresp.ServerError(w, ctx, "weather reverse geocode", err)
		return
	}
	httpresp.OK(w, "weather reverse results", geoPayload(results, status))
}

// geoPayload normalizes a geocode outcome for the wire: results is always an
// array (never null), including the disabled/unavailable cases.
func geoPayload(results []GeoResult, status Status) geoResponse {
	if results == nil {
		results = []GeoResult{}
	}
	return geoResponse{Results: results, Status: status}
}
