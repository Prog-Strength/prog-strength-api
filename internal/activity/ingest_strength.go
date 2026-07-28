package activity

import (
	"fmt"
	"io"
)

// ParseStrengthTCX turns a Garmin "Strength Training" TCX into an unsaved
// strength_training Activity summary plus the raw bytes for archiving. It is
// the strength-flavored counterpart of IngestTCX's front half: parse →
// strength-validate → summarize (distance-free). The activity type is fixed to
// strength_training regardless of the file's <Sport> tag, and the run
// summarizer's distance/pace/elevation/best-effort machinery is skipped
// entirely.
//
// Persistence happens separately: since the unified activity model the
// enrichment lands ON the workout's own base row via
// Repository.AttachStrengthEnrichment, so there is no repo call here.
//
// Returns:
//
//   - a *ValidationError with SlugParseFailed on a malformed file
//   - a *ValidationError with SlugNoEffortData when the file carries neither
//     HR nor calories
//
// The source is always IngestManualTCX — the only strength path today is a
// user-uploaded file. Unlike IngestTCX this never calls normalizeActivityType.
func ParseStrengthTCX(userID string, r io.Reader) (Activity, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Activity{}, nil, fmt.Errorf("activity: read tcx: %w", err)
	}

	parsed, err := parseTCX(data)
	if err != nil {
		return Activity{}, nil, validationErr(SlugParseFailed, err.Error())
	}
	if err := validateStrength(parsed); err != nil {
		return Activity{}, nil, err
	}

	a := summarizeStrength(parsed)
	a.UserID = userID
	a.IngestSource = IngestManualTCX
	return a, data, nil
}
