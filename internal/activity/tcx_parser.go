package activity

import (
	"encoding/xml"
	"fmt"
	"time"
)

// parsedTCX is the flattened, decoder-friendly view of a TCX file. The
// summarizer and validator both consume this rather than the raw XML
// structs so they never touch encoding/xml concerns.
type parsedTCX struct {
	Sport      string
	ActivityID string
	Notes      *string
	// Trackpoints is the ordered concatenation of every lap's track.
	// DistanceMeters is cumulative across the whole activity, exactly as
	// Garmin emits it (laps share one monotonic distance axis).
	Trackpoints []parsedTrackpoint
	// LapCalories holds the Calories element of each lap, in order. A lap
	// with no Calories element contributes nothing (see hasCalories).
	LapCalories []int
	// hasCalories is true iff at least one lap carried a Calories element.
	// We track presence separately from the sum because a real summed
	// value of 0 is meaningful, whereas "no calories reported" is nil.
	hasCalories bool
	// HasPosition is true iff at least one trackpoint carried a <Position>
	// element (GPS lat/lon). Used only at ingest to default a running
	// activity's environment (no position anywhere => indoor/treadmill). We
	// deliberately do not store the coordinates — route geometry is a
	// separate SOW; this is a presence bit, nothing more.
	HasPosition bool
	// LapStartTimes holds each lap's parsed StartTime attribute, in order.
	// Used by the strength summarizer as a start-time fallback when a file
	// somehow carries laps but no trackpoints. A lap whose StartTime is
	// absent or unparseable contributes nothing.
	LapStartTimes []time.Time
}

// parsedTrackpoint is one raw sample. Time is absolute (RFC3339 from the
// file); the summarizer converts to elapsed seconds against the first
// point. HR and Altitude are nil when the element was absent.
type parsedTrackpoint struct {
	Time           time.Time
	DistanceMeters float64
	HeartRateBpm   *int
	AltitudeMeters *float64
}

// The xml* structs below mirror only the subset of the Garmin TCX schema
// we need. They are private: nothing outside the parser should know the
// file is XML. encoding/xml matches by local element name and ignores the
// XML namespace, so the default-namespaced Garmin elements bind fine
// without us declaring the namespace here (verified in the parser test).
type xmlTrainingCenterDatabase struct {
	XMLName    xml.Name      `xml:"TrainingCenterDatabase"`
	Activities []xmlActivity `xml:"Activities>Activity"`
}

type xmlActivity struct {
	Sport string   `xml:"Sport,attr"`
	ID    string   `xml:"Id"`
	Notes *string  `xml:"Notes"`
	Laps  []xmlLap `xml:"Lap"`
}

type xmlLap struct {
	StartTime        string  `xml:"StartTime,attr"`
	TotalTimeSeconds float64 `xml:"TotalTimeSeconds"`
	DistanceMeters   float64 `xml:"DistanceMeters"`
	// Calories is a pointer so we can tell "absent" from "0".
	Calories    *int            `xml:"Calories"`
	Trackpoints []xmlTrackpoint `xml:"Track>Trackpoint"`
}

type xmlTrackpoint struct {
	Time           string        `xml:"Time"`
	DistanceMeters *float64      `xml:"DistanceMeters"`
	Position       *xmlPosition  `xml:"Position"`
	HeartRate      *xmlHeartRate `xml:"HeartRateBpm"`
	AltitudeMeters *float64      `xml:"AltitudeMeters"`
}

// xmlPosition models <Position><LatitudeDegrees>..</LatitudeDegrees>
// <LongitudeDegrees>..</LongitudeDegrees></Position>. We only need to know
// the element was present, so the fields are parsed but never read beyond a
// nil check on the pointer.
type xmlPosition struct {
	LatitudeDegrees  *float64 `xml:"LatitudeDegrees"`
	LongitudeDegrees *float64 `xml:"LongitudeDegrees"`
}

// xmlHeartRate models <HeartRateBpm><Value>148</Value></HeartRateBpm>.
// Garmin nests the number one level deep; we flatten it on parse.
type xmlHeartRate struct {
	Value int `xml:"Value"`
}

// parseTCX decodes a TCX byte slice into the flattened parsedTCX. It
// returns an error only for malformed XML or an unparseable timestamp;
// semantic checks (right sport, non-empty distance) belong to validate.
func parseTCX(data []byte) (*parsedTCX, error) {
	var doc xmlTrainingCenterDatabase
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse tcx: %w", err)
	}
	if len(doc.Activities) == 0 {
		// No <Activity> at all: treat as malformed rather than empty, as a
		// valid TCX always carries one. The validator turns this into a
		// parse-failed slug via the wrapped error path.
		return nil, fmt.Errorf("parse tcx: no activity found")
	}

	// We summarize a single activity per file (the import is one run). If a
	// file bundles several, take the first — Garmin exports one per file.
	act := doc.Activities[0]
	p := &parsedTCX{
		Sport:      act.Sport,
		ActivityID: act.ID,
		Notes:      act.Notes,
	}

	for _, lap := range act.Laps {
		if lap.Calories != nil {
			p.hasCalories = true
			p.LapCalories = append(p.LapCalories, *lap.Calories)
		}
		if lap.StartTime != "" {
			if t, err := time.Parse(time.RFC3339, lap.StartTime); err == nil {
				p.LapStartTimes = append(p.LapStartTimes, t)
			}
		}
		for _, tp := range lap.Trackpoints {
			t, err := time.Parse(time.RFC3339, tp.Time)
			if err != nil {
				return nil, fmt.Errorf("parse tcx: bad trackpoint time %q: %w", tp.Time, err)
			}
			var dist float64
			if tp.DistanceMeters != nil {
				dist = *tp.DistanceMeters
			}
			out := parsedTrackpoint{
				Time:           t,
				DistanceMeters: dist,
				AltitudeMeters: tp.AltitudeMeters,
			}
			if tp.HeartRate != nil {
				hr := tp.HeartRate.Value
				out.HeartRateBpm = &hr
			}
			if tp.Position != nil {
				p.HasPosition = true
			}
			p.Trackpoints = append(p.Trackpoints, out)
		}
	}

	return p, nil
}
