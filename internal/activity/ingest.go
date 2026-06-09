package activity

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// IngestTCX is the source-agnostic seam for landing a TCX file as an
// Activity. It owns the parse → validate → classify → summarize → persist
// pipeline so a future ingest path (Garmin Connect sync, Strava webhook,
// etc.) can reuse it without rewriting the middle.
//
// The function returns the persisted Activity (with generated ID, S3 key,
// and timestamps) on success. It returns:
//
//   - the wrapped error from parseTCX on a malformed file (callers checking
//     errors.As(err, &activity.ValidationError{}) get SlugParseFailed via
//     the wrapped path)
//   - a *ValidationError on a semantically empty TCX
//   - ErrDuplicate when the (userID, source, sourceActivityID) tuple
//     already exists in a live row
//   - ErrStorage when the archive Put fails
//   - any other error verbatim from the repository
//
// Known consistency gap: if the archive Put succeeds but the SQLite
// COMMIT fails, the orphaned S3 object is best-effort deleted by the
// SQLite repo. The narrower window — Put succeeded, the process crashes
// before Delete runs — leaves an orphan in the bucket and no row in the
// DB. We accept that for now (the user is single, the bucket is private,
// no reconciler is worth building yet) but it's the obvious next thing
// to address if this gets multi-tenant.
func IngestTCX(ctx context.Context, repo Repository, userID string, source IngestSource, r io.Reader) (Activity, error) {
	if !source.Valid() {
		return Activity{}, fmt.Errorf("activity: invalid ingest source %q", source)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return Activity{}, fmt.Errorf("activity: read tcx: %w", err)
	}

	parsed, err := parseTCX(data)
	if err != nil {
		return Activity{}, validationErr(SlugParseFailed, err.Error())
	}
	if err := validate(parsed); err != nil {
		return Activity{}, err
	}

	actType := normalizeActivityType(parsed.Sport, source)
	a := summarize(parsed, actType)
	a.UserID = userID
	a.IngestSource = source

	if err := repo.Create(ctx, &a, data); err != nil {
		if errors.Is(err, ErrDuplicate) {
			// Look up the existing live row so the caller can surface
			// its ID in the 409 response. A lookup failure here doesn't
			// turn into a different error class — the duplicate is the
			// load-bearing signal; the existing row is a convenience.
			if existing, lookupErr := repo.GetBySourceActivityID(ctx, userID, source, a.SourceActivityID); lookupErr == nil {
				return *existing, ErrDuplicate
			}
		}
		return Activity{}, err
	}
	return a, nil
}
