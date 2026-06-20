package activity

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// IngestStrengthTCX lands a Garmin "Strength Training" TCX as a
// strength_training Activity. It is the strength-flavored counterpart of
// IngestTCX: parse → strength-validate → summarize (distance-free) →
// persist. The activity type is fixed to strength_training regardless of the
// file's <Sport> tag, and the run summarizer's distance/pace/elevation/
// best-effort machinery is skipped entirely.
//
// It returns the persisted Activity (with generated ID, S3 key, timestamps)
// on success, or:
//
//   - a *ValidationError with SlugParseFailed on a malformed file
//   - a *ValidationError with SlugNoEffortData when the file carries neither
//     HR nor calories
//   - ErrDuplicate (with the existing live row, when the lookup succeeds)
//     when the (userID, manual_tcx, sourceActivityID) tuple already exists
//   - ErrStorage when the archive Put fails
//   - any other error verbatim from the repository
//
// The source is always IngestManualTCX — the only strength path today is a
// user-uploaded file. Unlike IngestTCX this never calls normalizeActivityType.
func IngestStrengthTCX(ctx context.Context, repo Repository, userID string, r io.Reader) (Activity, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Activity{}, fmt.Errorf("activity: read tcx: %w", err)
	}

	parsed, err := parseTCX(data)
	if err != nil {
		return Activity{}, validationErr(SlugParseFailed, err.Error())
	}
	if err := validateStrength(parsed); err != nil {
		return Activity{}, err
	}

	a := summarizeStrength(parsed)
	a.UserID = userID
	a.IngestSource = IngestManualTCX

	if err := repo.Create(ctx, &a, data); err != nil {
		if errors.Is(err, ErrDuplicate) {
			if existing, lookupErr := repo.GetBySourceActivityID(ctx, userID, IngestManualTCX, a.SourceActivityID); lookupErr == nil {
				return *existing, ErrDuplicate
			}
		}
		return Activity{}, err
	}
	return a, nil
}
