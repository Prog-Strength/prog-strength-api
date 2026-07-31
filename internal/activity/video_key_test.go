package activity

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func videoKeyTime(t *testing.T) time.Time {
	t.Helper()
	// Deliberately a non-UTC instant whose UTC date differs from its local
	// date, so the partition's UTC conversion is actually exercised.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return time.Date(2026, 7, 30, 21, 30, 0, 0, loc) // → 2026-07-31 01:30 UTC
}

func TestBuildVideoKey_LayoutAndUTCPartition(t *testing.T) {
	start := videoKeyTime(t)
	got, err := buildVideoKey("user1", ActivityHiking, start, "act1", "vid1", videoVariantVideo, "video/mp4")
	if err != nil {
		t.Fatalf("buildVideoKey: %v", err)
	}
	want := "user_id=user1/activity_type=hiking/year=2026/month=07/day=31/activity_id=act1/variant=video/vid1.mp4"
	if got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

func TestBuildVideoKey_ExtensionFollowsContainer(t *testing.T) {
	start := videoKeyTime(t)
	cases := map[string]string{
		"video/mp4":       "vid1.mp4",
		"video/quicktime": "vid1.mov",
	}
	for ct, suffix := range cases {
		got, err := buildVideoKey("user1", ActivityRunning, start, "act1", "vid1", videoVariantVideo, ct)
		if err != nil {
			t.Fatalf("%s: %v", ct, err)
		}
		if !strings.HasSuffix(got, suffix) {
			t.Errorf("%s → %q, want suffix %q", ct, got, suffix)
		}
	}
}

// The poster always comes out of the image pipeline, so its extension is fixed
// regardless of the video's container.
func TestBuildVideoKey_PosterIsAlwaysJPEG(t *testing.T) {
	start := videoKeyTime(t)
	got, err := buildVideoKey("user1", ActivityRunning, start, "act1", "vid1", videoVariantPoster, "video/quicktime")
	if err != nil {
		t.Fatalf("buildVideoKey: %v", err)
	}
	if !strings.HasSuffix(got, "/variant=poster/vid1.jpg") {
		t.Errorf("poster key = %q, want a .jpg under variant=poster", got)
	}
}

// An unknown container must not reach the key — the extension is looked up
// from a closed map, never derived from a client-supplied filename.
func TestBuildVideoKey_RejectsUnknownContainer(t *testing.T) {
	start := videoKeyTime(t)
	_, err := buildVideoKey("user1", ActivityRunning, start, "act1", "vid1", videoVariantVideo, "video/x-matroska")
	if !errors.Is(err, ErrInvalidVideoExtension) {
		t.Errorf("err = %v, want ErrInvalidVideoExtension", err)
	}
}

// Path-traversal and Hive-layout-breaking characters are rejected in every id
// position, mirroring buildPhotoKey.
func TestBuildVideoKey_RejectsUnsafeIDParts(t *testing.T) {
	start := videoKeyTime(t)
	bad := []string{"../etc", "a/b", "a=b", "a b", "a.b"}
	for _, s := range bad {
		if _, err := buildVideoKey(s, ActivityRunning, start, "act1", "vid1", videoVariantVideo, "video/mp4"); !errors.Is(err, ErrInvalidKeyPart) {
			t.Errorf("user_id %q: err = %v, want ErrInvalidKeyPart", s, err)
		}
		if _, err := buildVideoKey("user1", ActivityRunning, start, s, "vid1", videoVariantVideo, "video/mp4"); !errors.Is(err, ErrInvalidKeyPart) {
			t.Errorf("activity_id %q: err = %v, want ErrInvalidKeyPart", s, err)
		}
		if _, err := buildVideoKey("user1", ActivityRunning, start, "act1", s, videoVariantVideo, "video/mp4"); !errors.Is(err, ErrInvalidKeyPart) {
			t.Errorf("video_id %q: err = %v, want ErrInvalidKeyPart", s, err)
		}
	}
}

// This pins the trap documented in adding-an-activity-type.md: the key builder
// gates on ActivityType.Valid(), a HAND-MAINTAINED switch. A type that is
// registered in the Go registry but missing from that switch fails key building
// — which the upload handler surfaces as a 500. The test exists so the failure
// mode is discoverable here rather than in production.
func TestBuildVideoKey_RejectsTypeMissingFromValidSwitch(t *testing.T) {
	start := videoKeyTime(t)
	_, err := buildVideoKey("user1", ActivityType("kickboxing"), start, "act1", "vid1", videoVariantVideo, "video/mp4")
	if !errors.Is(err, ErrInvalidActivityType) {
		t.Errorf("err = %v, want ErrInvalidActivityType", err)
	}
}

// Every type currently in Valid() must build a key — otherwise videos are
// silently broken for that activity type.
func TestBuildVideoKey_AcceptsEveryRegisteredType(t *testing.T) {
	start := videoKeyTime(t)
	for _, at := range []ActivityType{
		ActivityRunning, ActivityWalking, ActivityCycling,
		ActivityHiking, ActivityOther, ActivityStrengthTraining,
	} {
		if _, err := buildVideoKey("user1", at, start, "act1", "vid1", videoVariantVideo, "video/mp4"); err != nil {
			t.Errorf("%s: %v", at, err)
		}
	}
}
