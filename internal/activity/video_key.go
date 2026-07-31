package activity

import (
	"errors"
	"fmt"
	"time"
)

// videoVariant is the closed set of objects archived per uploaded video: the
// stored original and its poster frame. Unlike photoVariant these are NOT two
// renditions of one pipeline output — the video is stored byte-identical to the
// upload and the poster is a client-generated JPEG — so a video may legitimately
// exist with a video object and no poster (see 048_activity_videos.sql).
type videoVariant string

const (
	videoVariantVideo  videoVariant = "video"
	videoVariantPoster videoVariant = "poster"
)

// ErrInvalidVideoVariant is returned by buildVideoKey when variant is not one
// of the closed videoVariant members.
var ErrInvalidVideoVariant = errors.New("activity: invalid video variant")

// ErrInvalidVideoExtension is returned when the derived file extension is not
// in the closed allowlist. The extension comes from the upload's content type,
// which is attacker-influenced in principle, and it lands in an S3 key — so it
// is validated rather than interpolated.
var ErrInvalidVideoExtension = errors.New("activity: invalid video extension")

// videoExtensions maps an accepted container content type to its file
// extension. Keys must stay in sync with videos.allowed_content_types in
// config.toml; a type accepted by config but missing here fails key building
// rather than producing an extensionless object.
var videoExtensions = map[string]string{
	"video/mp4":       "mp4",
	"video/quicktime": "mov",
}

// buildVideoKey returns the Hive-partitioned S3 key for an activity video
// object. The layout deliberately mirrors buildPhotoKey exactly:
//
//	user_id={user_id}/activity_type={type}/year={yyyy}/month={mm}/day={dd}/activity_id={activity_id}/variant={variant}/{video_id}.{ext}
//
// Same reasoning as photos, unchanged: Hive partitioning is Athena/Glue
// readable as written; the date partition is the activity's UTC start time so
// a display-timezone change never reshuffles the bucket; and variant is its own
// partition level so "every poster" is a prefix scan rather than a filtered
// full listing.
//
// Unlike photos the extension is NOT fixed, because nothing is re-encoded — the
// stored object keeps the uploaded container. It is looked up from the closed
// videoExtensions map rather than derived from the filename, so a hostile
// filename can never reach the key.
//
// > Carries the photo trap forward: activityType is validated against
// > ActivityType.Valid(), which is a HAND-MAINTAINED switch. A newly registered
// > activity type that isn't added there fails key building, and the upload
// > handler surfaces that as a 500. See adding-an-activity-type.md, "What does
// > NOT come free".
func buildVideoKey(userID string, activityType ActivityType, activityStart time.Time, activityID, videoID string, variant videoVariant, contentType string) (string, error) {
	if !idPartPattern.MatchString(userID) {
		return "", fmt.Errorf("%w: user_id %q", ErrInvalidKeyPart, userID)
	}
	if !idPartPattern.MatchString(activityID) {
		return "", fmt.Errorf("%w: activity_id %q", ErrInvalidKeyPart, activityID)
	}
	if !idPartPattern.MatchString(videoID) {
		return "", fmt.Errorf("%w: video_id %q", ErrInvalidKeyPart, videoID)
	}
	if !activityType.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidActivityType, activityType)
	}

	var ext string
	switch variant {
	case videoVariantVideo:
		e, ok := videoExtensions[contentType]
		if !ok {
			return "", fmt.Errorf("%w: content type %q", ErrInvalidVideoExtension, contentType)
		}
		ext = e
	case videoVariantPoster:
		// The poster always goes through the image pipeline, which always emits
		// JPEG — same totality the photo key builder relies on.
		ext = "jpg"
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidVideoVariant, variant)
	}

	d := activityStart.UTC()
	return fmt.Sprintf(
		"user_id=%s/activity_type=%s/year=%04d/month=%02d/day=%02d/activity_id=%s/variant=%s/%s.%s",
		userID, activityType, d.Year(), d.Month(), d.Day(), activityID, variant, videoID, ext,
	), nil
}
