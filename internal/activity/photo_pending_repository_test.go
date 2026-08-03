package activity

import (
	"context"
	"errors"
	"testing"
	"time"
)

// reserve inserts a row in the state the two-phase upload's first step leaves
// it in: pending, with the placeholder keys and dimensions the migration's
// NOT NULL columns require.
func reservePhoto(t *testing.T, r *SQLitePhotoRepository, userID, activityID string) ActivityPhoto {
	t.Helper()
	p := newPhoto(userID, activityID)
	p.Status = PhotoStatusPending
	// A reservation has no objects yet, but 047's NOT NULL constraints still
	// demand values — hence the placeholders the worker overwrites at commit.
	// (Blanking the key fields trips gitleaks' generic-api-key rule, which
	// reads `S3Key = ""` as a credential assignment; it is the opposite.)
	p.S3Key, p.ThumbS3Key = "", "" //gitleaks:allow
	p.ByteSize, p.Width, p.Height = 0, 0, 0
	stored, err := r.Insert(context.Background(), p)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return stored
}

// The synchronous path knows nothing about states, so a row it writes must
// still land renderable. This is what lets the old endpoint stay mounted.
func TestInsertDefaultsToReadyForTheSynchronousPath(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")

	stored, err := r.Insert(context.Background(), newPhoto("u1", aid))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != PhotoStatusReady {
		t.Errorf("status = %q, want %q", stored.Status, PhotoStatusReady)
	}

	got, err := r.ListByActivity(context.Background(), "u1", aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d photos, want 1", len(got))
	}
}

// A reservation is not a photo. It must not appear anywhere the UI reads,
// because the object behind it may never arrive.
func TestPendingReservationsAreInvisibleToReads(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()

	list, err := r.ListByActivity(ctx, "u1", aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("ListByActivity returned %d rows, want 0", len(list))
	}
	if _, getErr := r.Get(ctx, "u1", aid, res.ID); !errors.Is(getErr, ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", getErr)
	}
	ids, err := r.LiveIDs(ctx, "u1", aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("LiveIDs returned %v, want none", ids)
	}

	// ...but it DOES hold a slot, or a user could reserve past the cap.
	n, err := r.CountLive(ctx, aid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CountLive = %d, want 1 — a reservation occupies a slot", n)
	}
}

// A processing photo is shown as a placeholder in the strip, so reads must
// return it — but it has no URLs, so it must never win a timeline cover.
func TestProcessingIsVisibleInTheStripButNeverACover(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()

	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 4096); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListByActivity(ctx, "u1", aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != PhotoStatusProcessing {
		t.Fatalf("strip read = %+v, want one processing row", list)
	}

	covers, err := r.CoverPhotosByActivityIDs(ctx, []string{aid})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := covers[aid]; ok {
		t.Error("a processing photo became a timeline cover; it has no URLs to resolve")
	}
}

// Commit is not idempotent by accident — the status guard is what makes a
// replayed request a rejection rather than a second hand-off to the worker.
func TestMarkProcessingRejectsReplayAndForeignOwners(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()

	if err := r.MarkProcessing(ctx, "u2", aid, res.ID, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign owner err = %v, want ErrNotFound", err)
	}
	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 10); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("replayed commit err = %v, want ErrNotFound", err)
	}
}

// The claim increments on the way IN. A worker killed mid-photo (OOM,
// termination) never records anything on the way out, so counting there would
// let a poison image be retried forever.
func TestClaimIncrementsAttemptsAndStopsAtTheCap(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()
	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 4096); err != nil {
		t.Fatal(err)
	}

	const maxAttempts = 3
	for i := 1; i <= maxAttempts; i++ {
		got, ok, err := r.ClaimNextForProcessing(ctx, maxAttempts)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("claim %d: got ok=false, want a row", i)
		}
		if got.Attempts != i {
			t.Errorf("claim %d: attempts = %d, want %d", i, got.Attempts, i)
		}
	}

	if _, ok, err := r.ClaimNextForProcessing(ctx, maxAttempts); err != nil || ok {
		t.Errorf("claim past cap: ok = %v, err = %v; want ok=false", ok, err)
	}
}

func TestClaimReturnsNothingWhenNoRowIsProcessing(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	reservePhoto(t, r, "u1", aid) // pending, not processing

	_, ok, err := r.ClaimNextForProcessing(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("claimed a pending reservation; only processing rows are the worker's")
	}
}

// MarkReady is the moment the photo becomes real. It must also clear
// upload_s3_key: the staged original is deleted by then, so leaving the key
// set would name an object that no longer exists.
func TestMarkReadyPublishesTheRowAndForgetsTheStagedKey(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()

	if err := r.SetUploadKey(ctx, res.ID, "uploads/u1/abc.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 4096); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkReady(ctx, res.ID, "photos/full.jpg", "photos/thumb.jpg", 5000, 4032, 3024); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(ctx, "u1", aid, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PhotoStatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if got.S3Key != "photos/full.jpg" || got.ThumbS3Key != "photos/thumb.jpg" {
		t.Errorf("keys = %q / %q", got.S3Key, got.ThumbS3Key)
	}
	if got.Width != 4032 || got.Height != 3024 || got.ByteSize != 5000 {
		t.Errorf("dimensions/size = %dx%d %d", got.Width, got.Height, got.ByteSize)
	}
	if got.UploadS3Key != nil {
		t.Errorf("upload_s3_key = %q, want NULL once the staged object is gone", *got.UploadS3Key)
	}

	covers, err := r.CoverPhotosByActivityIDs(ctx, []string{aid})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := covers[aid]; !ok {
		t.Error("a ready photo should be eligible as a timeline cover")
	}
}

// A failed render is not a photo and must not occupy a slot forever, or a
// handful of bad uploads would permanently consume the per-activity cap.
func TestMarkFailedHidesTheRowAndFreesItsSlot(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	res := reservePhoto(t, r, "u1", aid)
	ctx := context.Background()
	if err := r.MarkProcessing(ctx, "u1", aid, res.ID, 4096); err != nil {
		t.Fatal(err)
	}

	if err := r.MarkFailed(ctx, res.ID, "decode: not an image"); err != nil {
		t.Fatal(err)
	}

	list, err := r.ListByActivity(ctx, "u1", aid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("failed row is visible in the strip: %+v", list)
	}
	n, err := r.CountLive(ctx, aid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("CountLive = %d, want 0 — a failed render must free its slot", n)
	}
}

func TestExpiredPendingFindsOnlyStaleReservations(t *testing.T) {
	r, dbc := newPhotoRepo(t)
	aid := insertTestActivity(t, dbc, "u1")
	ctx := context.Background()

	stale := reservePhoto(t, r, "u1", aid)
	fresh := reservePhoto(t, r, "u1", aid)
	committed := reservePhoto(t, r, "u1", aid)
	if err := r.MarkProcessing(ctx, "u1", aid, committed.ID, 1); err != nil {
		t.Fatal(err)
	}
	// Age the first reservation past any plausible presign TTL.
	if _, err := dbc.ExecContext(ctx,
		`UPDATE activity_photo SET created_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour), stale.ID); err != nil {
		t.Fatal(err)
	}

	got, err := r.ExpiredPending(ctx, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != stale.ID {
		t.Fatalf("ExpiredPending = %d rows, want only the stale reservation "+
			"(fresh=%s committed=%s)", len(got), fresh.ID, committed.ID)
	}
}
