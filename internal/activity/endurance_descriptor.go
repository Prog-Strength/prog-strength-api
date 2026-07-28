package activity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// EnduranceDetails is the detail representation shared by every
// endurance-shaped type (running/walking/cycling/other): exactly the columns
// of the type's activity_*_details table. It doubles as the Details JSON
// shape a manual endurance create will carry.
type EnduranceDetails struct {
	DistanceMeters      float64     `json:"distance_meters"`
	RawDistanceMeters   float64     `json:"raw_distance_meters,omitempty"`
	AvgPaceSecPerKm     *float64    `json:"avg_pace_sec_per_km,omitempty"`
	BestPaceSecPerKm    *float64    `json:"best_pace_sec_per_km,omitempty"`
	ElevationGainMeters *float64    `json:"elevation_gain_meters,omitempty"`
	Environment         Environment `json:"environment,omitempty"`
	RouteGeoJSON        *string     `json:"route_geojson,omitempty"`
}

// enduranceLabel is the default card title per sport, used when the activity
// has no (or a blank) name. Mirrors the timeline hydrator's run card.
func enduranceLabel(t ActivityType) string {
	switch t {
	case ActivityRunning:
		return "Run"
	case ActivityWalking:
		return "Walk"
	case ActivityCycling:
		return "Ride"
	}
	return "Activity"
}

// NewEnduranceDescriptor builds the descriptor for one endurance-shaped type.
// All four types share the same detail shape and summarizer; only the label
// and the backing detail table (inside store) differ. store may be nil in
// tests that only exercise validation/summaries.
//
// MountRoutes stays nil here: only running has type-specific routes
// (calibrate), assigned in server.go from the activity handler. The TCX
// upload and the rename/environment PATCH remain on the base handler — the
// former is cross-type endurance ingest (the file's <Sport> tag picks the
// type), the latter is shared by every type.
func NewEnduranceDescriptor(t ActivityType, store DetailStore) *Descriptor {
	return &Descriptor{
		Type:           t,
		ValidateCreate: enduranceValidateCreate(t),
		Details:        store,
		Summarize:      enduranceSummarize(t),
		DecodeDetails:  decodeEnduranceDetails,
	}
}

// decodeEnduranceDetails is the generic-write decode step: parse the blob
// and default RawDistanceMeters to the entered distance, preserving the
// "raw equals distance at ingest" invariant for manual logs (calibration
// later diverges the pair).
func decodeEnduranceDetails(raw json.RawMessage) (any, error) {
	d, err := parseEnduranceDetails(raw)
	if err != nil {
		return nil, err
	}
	if d == nil {
		// A typed nil inside `any` would read as non-nil to the caller.
		return nil, nil
	}
	if d.RawDistanceMeters == 0 {
		d.RawDistanceMeters = d.DistanceMeters
	}
	return d, nil
}

// enduranceValidateCreate checks a manual endurance create's Details blob.
// The one structural rule (per the SOW: "runs require distance") is a
// positive distance for the distance-first sports; ActivityOther tolerates
// absent details — a zero-distance session is a legitimate "other" entry
// (the detail row's distance column defaults to 0). Failures wrap the
// package sentinels (ErrDistanceRequired / ErrInvalidDetails) so the
// handler's error mapping is errors.Is-testable, matching the strength
// descriptor's sentinel style.
func enduranceValidateCreate(t ActivityType) func(req CreateRequest) error {
	requireDistance := t != ActivityOther
	return func(req CreateRequest) error {
		d, err := parseEnduranceDetails(req.Details)
		if err != nil {
			return err
		}
		if d == nil {
			if requireDistance {
				return fmt.Errorf("%w: %s details need a positive distance_meters", ErrDistanceRequired, t)
			}
			return nil
		}
		if requireDistance && d.DistanceMeters <= 0 {
			return fmt.Errorf("%w: %s details need a positive distance_meters", ErrDistanceRequired, t)
		}
		if d.DistanceMeters < 0 {
			return fmt.Errorf("%w: distance_meters must not be negative", ErrInvalidDetails)
		}
		if d.Environment != "" && !d.Environment.Valid() {
			return fmt.Errorf("%w: invalid environment %q (valid: outdoor, indoor)", ErrInvalidDetails, d.Environment)
		}
		return nil
	}
}

// parseEnduranceDetails decodes a Details blob, treating absent/JSON-null as
// no details. Unknown fields are rejected so a typoed metric fails loudly
// instead of silently dropping data. Decode failures wrap ErrInvalidDetails.
func parseEnduranceDetails(raw json.RawMessage) (*EnduranceDetails, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d EnduranceDetails
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDetails, err)
	}
	return &d, nil
}

// enduranceSummarize renders the endurance card: "5.0 mi · 41:12" — the same
// shape the timeline hydrator renders for run posts. Distance prefers the
// loaded details (authoritative) and falls back to the base Activity's
// joined-in endurance fields; duration always lives on the base row. A
// zero distance drops the distance chip entirely ("1:00:00", never
// "0.0 mi · 1:00:00") — only duration-only ActivityOther entries hit this
// in practice, since the distance-first sports validate distance > 0.
func enduranceSummarize(t ActivityType) func(a Activity, details any) Summary {
	label := enduranceLabel(t)
	return func(a Activity, details any) Summary {
		title := label
		if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
			title = *a.Name
		}
		meters := a.DistanceMeters
		if d, ok := details.(*EnduranceDetails); ok && d != nil {
			meters = d.DistanceMeters
		}
		duration := FormatDuration(float64(a.DurationSeconds))
		if meters <= 0 {
			return Summary{
				Title:    title,
				Subtitle: duration,
				Metrics:  []string{duration},
			}
		}
		distance := FormatMiles(meters)
		return Summary{
			Title:    title,
			Subtitle: distance + " · " + duration,
			Metrics:  []string{distance, duration},
		}
	}
}
