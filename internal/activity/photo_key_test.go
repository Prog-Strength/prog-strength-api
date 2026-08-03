package activity

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildPhotoKey(t *testing.T) {
	t.Parallel()

	// pacific is the user's local zone for the boundary-test cases. A
	// timestamp that's "yesterday" in Pacific can be "today" in UTC, and
	// we want to confirm the key uses the UTC day.
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load pacific: %v", err)
	}

	cases := []struct {
		name    string
		userID  string
		actType ActivityType
		start   time.Time
		actID   string
		photoID string
		variant photoVariant
		want    string
		wantErr error
	}{
		{
			name:    "full variant normal case",
			userID:  "user_abc123",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 14, 30, 0, 0, time.UTC),
			actID:   "act_xyz789",
			photoID: "photo_001",
			variant: photoVariantFull,
			want:    "user_id=user_abc123/activity_type=running/year=2026/month=06/day=08/activity_id=act_xyz789/variant=full/photo_001.jpg",
		},
		{
			name:    "thumb variant normal case",
			userID:  "user_abc123",
			actType: ActivityHiking,
			start:   time.Date(2026, 6, 8, 14, 30, 0, 0, time.UTC),
			actID:   "act_xyz789",
			photoID: "photo_001",
			variant: photoVariantThumb,
			want:    "user_id=user_abc123/activity_type=hiking/year=2026/month=06/day=08/activity_id=act_xyz789/variant=thumb/photo_001.jpg",
		},
		{
			name:    "single-digit month and day pad to two",
			userID:  "u1",
			actType: ActivityCycling,
			start:   time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			want:    "user_id=u1/activity_type=cycling/year=2026/month=01/day=03/activity_id=a1/variant=full/p1.jpg",
		},
		{
			name:    "year zero-pads to four digits",
			userID:  "u1",
			actType: ActivityOther,
			start:   time.Date(999, 12, 31, 23, 59, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantThumb,
			want:    "user_id=u1/activity_type=other/year=0999/month=12/day=31/activity_id=a1/variant=thumb/p1.jpg",
		},
		{
			name:    "non-UTC zone partitions on UTC date",
			userID:  "u1",
			actType: ActivityRunning,
			// 2026-06-07T22:00:00-07:00 == 2026-06-08T05:00:00Z: local day is
			// the 7th, UTC day is the 8th. The key must partition on the 8th.
			start:   time.Date(2026, 6, 7, 22, 0, 0, 0, time.FixedZone("PDT", -7*3600)),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			want:    "user_id=u1/activity_type=running/year=2026/month=06/day=08/activity_id=a1/variant=full/p1.jpg",
		},
		{
			name:    "pacific late evening is next UTC day",
			userID:  "u1",
			actType: ActivityRunning,
			// 2026-06-07T22:00:00-07:00 == 2026-06-08T05:00:00Z
			start:   time.Date(2026, 6, 7, 22, 0, 0, 0, pacific),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantThumb,
			want:    "user_id=u1/activity_type=running/year=2026/month=06/day=08/activity_id=a1/variant=thumb/p1.jpg",
		},

		// --- userID rejections ---
		{
			name:    "userID with slash rejected",
			userID:  "user/abc",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with equals rejected",
			userID:  "user=abc",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with dot rejected",
			userID:  "user.abc",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with whitespace rejected",
			userID:  "user abc",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "empty userID rejected",
			userID:  "",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},

		// --- activityID rejections ---
		{
			name:    "activityID with slash rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a/1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "activityID with equals rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a=1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "activityID with dot rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a.1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "activityID with whitespace rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a 1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "empty activityID rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},

		// --- photoID rejections ---
		{
			name:    "photoID with slash rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p/1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "photoID with equals rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p=1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "photoID with dot rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p.1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "photoID with whitespace rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p 1",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "empty photoID rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "",
			variant: photoVariantFull,
			wantErr: ErrInvalidKeyPart,
		},

		// --- activityType rejections ---
		{
			name:    "invalid activityType rejected",
			userID:  "u1",
			actType: ActivityType("swimming"),
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidActivityType,
		},
		{
			name:    "empty activityType rejected",
			userID:  "u1",
			actType: ActivityType(""),
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariantFull,
			wantErr: ErrInvalidActivityType,
		},

		// --- variant rejections ---
		{
			name:    "invalid variant rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariant("original"),
			wantErr: ErrInvalidVariant,
		},
		{
			name:    "empty variant rejected",
			userID:  "u1",
			actType: ActivityRunning,
			start:   time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			photoID: "p1",
			variant: photoVariant(""),
			wantErr: ErrInvalidVariant,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildPhotoKey(c.userID, c.actType, c.start, c.actID, c.photoID, c.variant)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Errorf("buildPhotoKey =\n  %q\nwant\n  %q", got, c.want)
			}
		})
	}
}

// The staged upload key must land under the prefix the bucket's lifecycle
// rule reaps, and must not collide with the serving variants.
func TestBuildPhotoUploadKey(t *testing.T) {
	start := time.Date(2026, 8, 2, 14, 30, 0, 0, time.UTC)

	got, err := buildPhotoUploadKey("u1", ActivityRunning, start, "a1", "p1", "image/jpeg")
	if err != nil {
		t.Fatalf("buildPhotoUploadKey: %v", err)
	}

	if !strings.HasPrefix(got, "uploads/") {
		t.Errorf("key %q does not start with uploads/ — the lifecycle rule filters on that prefix", got)
	}
	if !strings.Contains(got, "variant=original") {
		t.Errorf("key %q should mark itself as the original", got)
	}
	full, err := buildPhotoKey("u1", ActivityRunning, start, "a1", "p1", photoVariantFull)
	if err != nil {
		t.Fatal(err)
	}
	if got == full {
		t.Error("staged key collides with the serving key; the stripped copy would overwrite the original")
	}
	if strings.HasPrefix(full, "uploads/") {
		t.Error("a SERVING key landed under uploads/, where the lifecycle rule would reap it")
	}
}

func TestBuildPhotoUploadKeyExtensionPerContentType(t *testing.T) {
	start := time.Date(2026, 8, 2, 14, 30, 0, 0, time.UTC)
	for ct, wantExt := range map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	} {
		got, err := buildPhotoUploadKey("u1", ActivityRunning, start, "a1", "p1", ct)
		if err != nil {
			t.Fatalf("%s: %v", ct, err)
		}
		if !strings.HasSuffix(got, wantExt) {
			t.Errorf("%s -> %q, want suffix %q", ct, got, wantExt)
		}
	}
	if _, err := buildPhotoUploadKey("u1", ActivityRunning, start, "a1", "p1", "image/gif"); err == nil {
		t.Error("accepted an unsupported content type")
	}
}
