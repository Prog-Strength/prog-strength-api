package activity

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// idPartPattern is the allowed character set for the user_id and
// activity_id components of a TCX S3 key. Restricting to URL-safe
// segment characters keeps Hive-partitioned listing tools (Athena, the
// AWS CLI's `--prefix`, simple s3 ls greps) unambiguous: a literal "/"
// would create a fake partition level, "=" would confuse the
// {key}={value} convention, and "." would invite path-traversal-style
// patterns.
var idPartPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrInvalidKeyPart is returned by buildTCXKey when userID or activityID
// fails the idPartPattern check. The caller's only sane reaction is to
// fail the ingest — the key is part of a transactional write.
var ErrInvalidKeyPart = errors.New("activity: invalid s3 key part")

// ErrInvalidActivityType is returned by buildTCXKey when activityType is
// not one of the closed enum values.
var ErrInvalidActivityType = errors.New("activity: invalid activity type")

// buildTCXKey returns the Hive-partitioned S3 key under which a raw TCX
// file is archived. The layout is:
//
//	user_id={user_id}/activity_type={type}/year={yyyy}/month={mm}/day={dd}/{activity_id}.tcx
//
// Hive partitioning ("key=value" path segments) is the standard layout
// Athena, Glue, and aws s3api list-objects understand without extra
// schema config — when we later want to query "all my 2026 runs" from
// a notebook, this scheme is queryable as-is.
//
// The date partition uses the activity's start time converted to UTC.
// UTC is the right choice for the partition (not the user's local time)
// because S3 keys are global, the user's timezone preference can change,
// and a future migration to a different display zone shouldn't reshuffle
// the bucket. Display-time zone conversion stays at the read boundary
// (the handler / frontend), where it belongs.
//
// Returns ErrInvalidKeyPart when userID or activityID contains a
// character outside ^[A-Za-z0-9_-]+$ (the slash, equals sign, dot, and
// whitespace are the load-bearing rejections — they would break the
// Hive layout or invite path traversal). Returns ErrInvalidActivityType
// when activityType is not a known enum member.
func buildTCXKey(userID string, activityType ActivityType, activityDate time.Time, activityID string) (string, error) {
	if !idPartPattern.MatchString(userID) {
		return "", fmt.Errorf("%w: user_id %q", ErrInvalidKeyPart, userID)
	}
	if !idPartPattern.MatchString(activityID) {
		return "", fmt.Errorf("%w: activity_id %q", ErrInvalidKeyPart, activityID)
	}
	if !activityType.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidActivityType, activityType)
	}
	d := activityDate.UTC()
	return fmt.Sprintf(
		"user_id=%s/activity_type=%s/year=%04d/month=%02d/day=%02d/%s.tcx",
		userID, activityType, d.Year(), d.Month(), d.Day(), activityID,
	), nil
}
