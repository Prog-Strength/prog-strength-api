package activity

import (
	"errors"
	"fmt"
	"time"
)

// photoVariant is the closed set of rendered sizes we archive for each
// uploaded activity photo. The server re-encodes every upload into exactly
// these variants (a display-sized "full" and a small "thumb"), so a photo
// always exists at both variants or neither.
type photoVariant string

const (
	photoVariantFull  photoVariant = "full"
	photoVariantThumb photoVariant = "thumb"
)

// ErrInvalidVariant is returned by buildPhotoKey when variant is not one
// of the closed photoVariant members.
var ErrInvalidVariant = errors.New("activity: invalid photo variant")

// buildPhotoKey returns the Hive-partitioned S3 key under which a rendered
// activity photo is archived. The layout is:
//
//	user_id={user_id}/activity_type={type}/year={yyyy}/month={mm}/day={dd}/activity_id={activity_id}/variant={variant}/{photo_id}.jpg
//
// Hive partitioning ("key=value" path segments) is the standard layout
// Athena, Glue, and aws s3api list-objects understand without extra schema
// config — when we later want to query "all thumbnails for my 2026 runs"
// from a notebook, this scheme is queryable as-is.
//
// The date partition uses the activity's start time converted to UTC. UTC
// is the right choice for the partition (not the user's local time) because
// S3 keys are global, the user's timezone preference can change, and a
// future migration to a different display zone shouldn't reshuffle the
// bucket. Display-time zone conversion stays at the read boundary (the
// handler / frontend), where it belongs.
//
// variant is its own partition level (rather than, say, a filename suffix)
// so that a full-vs-thumb listing is a cheap prefix scan: fetching every
// thumbnail for a lifecycle/cleanup job, or serving only thumbs to a grid
// view, is a single `--prefix .../variant=thumb/` request instead of a
// full listing filtered client-side.
//
// The key is always ".jpg" because the server re-encodes every upload to
// JPEG before storing it; the builder is therefore total in the extension
// and never has to inspect the source content type. The photoID carries no
// extension — the variant partition and the fixed .jpg suffix fully
// describe the object.
//
// Returns ErrInvalidKeyPart when userID, activityID, or photoID contains a
// character outside ^[A-Za-z0-9_-]+$ (the slash, equals sign, dot, and
// whitespace are the load-bearing rejections — they would break the Hive
// layout or invite path traversal). Returns ErrInvalidActivityType when
// activityType is not a known enum member, and ErrInvalidVariant when
// variant is not one of the closed photoVariant members.
func buildPhotoKey(userID string, activityType ActivityType, activityStart time.Time, activityID, photoID string, variant photoVariant) (string, error) {
	if !idPartPattern.MatchString(userID) {
		return "", fmt.Errorf("%w: user_id %q", ErrInvalidKeyPart, userID)
	}
	if !idPartPattern.MatchString(activityID) {
		return "", fmt.Errorf("%w: activity_id %q", ErrInvalidKeyPart, activityID)
	}
	if !idPartPattern.MatchString(photoID) {
		return "", fmt.Errorf("%w: photo_id %q", ErrInvalidKeyPart, photoID)
	}
	if !activityType.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidActivityType, activityType)
	}
	if variant != photoVariantFull && variant != photoVariantThumb {
		return "", fmt.Errorf("%w: %q", ErrInvalidVariant, variant)
	}
	d := activityStart.UTC()
	return fmt.Sprintf(
		"user_id=%s/activity_type=%s/year=%04d/month=%02d/day=%02d/activity_id=%s/variant=%s/%s.jpg",
		userID, activityType, d.Year(), d.Month(), d.Day(), activityID, variant, photoID,
	), nil
}
