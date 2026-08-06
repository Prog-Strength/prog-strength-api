package activity

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// newVideoRepo returns a video repository backed by a fresh migrated DB,
// sharing the same *sql.DB so the test can insert parent activities directly.
// Mirrors newPhotoRepo.
func newVideoRepo(t *testing.T) (*SQLiteVideoRepository, *sql.DB) {
	t.Helper()
	dbc := newMigratedDB(t)
	return NewSQLiteVideoRepository(dbc), dbc
}

// newVideo builds a minimal valid pending ActivityVideo. ByteSize is 0 at
// reservation time on purpose — the real size arrives from S3 at commit.
func newVideo(userID, activityID string) ActivityVideo {
	return ActivityVideo{
		ActivityID:  activityID,
		UserID:      userID,
		S3Key:       "video/" + id.New() + ".mp4",
		ContentType: "video/mp4",
	}
}

// A reserved video is invisible to reads until it is committed. This is the
// core of the two-phase upload: the row exists before the object does, and a
// client that vanishes mid-upload must never leave a broken entry in the strip.
func TestVideoRepository_PendingIsInvisibleUntilReady(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	v, err := repo.Insert(ctx, newVideo(userID, activityID))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if v.Status != VideoStatusPending {
		t.Errorf("status = %q, want %q", v.Status, VideoStatusPending)
	}

	got, err := repo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("pending video is visible in ListByActivity: %d rows", len(got))
	}
	// But it DOES hold a slot against the per-activity cap.
	n, err := repo.CountLive(ctx, activityID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("CountLive = %d, want 1 (a reservation holds its slot)", n)
	}

	poster := "poster/x.jpg"
	if readyErr := repo.MarkReady(ctx, userID, activityID, v.ID, 987654, &poster); readyErr != nil {
		t.Fatalf("mark ready: %v", readyErr)
	}

	got, err = repo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		t.Fatalf("list after ready: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ready video count = %d, want 1", len(got))
	}
	if got[0].ByteSize != 987654 {
		t.Errorf("byte_size = %d, want the S3-confirmed 987654", got[0].ByteSize)
	}
	if got[0].PosterS3Key == nil || *got[0].PosterS3Key != poster {
		t.Errorf("poster_s3_key = %v, want %q", got[0].PosterS3Key, poster)
	}
}

// Committing twice must not silently succeed: MarkReady is scoped to pending
// rows, so a replayed commit is a miss rather than an overwrite.
func TestVideoRepository_MarkReadyIsNotReplayable(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	v, err := repo.Insert(ctx, newVideo(userID, activityID))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if readyErr := repo.MarkReady(ctx, userID, activityID, v.ID, 100, nil); readyErr != nil {
		t.Fatalf("first mark ready: %v", readyErr)
	}
	err = repo.MarkReady(ctx, userID, activityID, v.ID, 999, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second MarkReady error = %v, want ErrNotFound", err)
	}
}

// A poster that never materialized is not a reason to lose the video — the
// column is nullable and a null must survive the round trip.
func TestVideoRepository_ReadyWithoutPoster(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	v, _ := repo.Insert(ctx, newVideo(userID, activityID))
	if err := repo.MarkReady(ctx, userID, activityID, v.ID, 42, nil); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	got, err := repo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("count = %d, want 1", len(got))
	}
	if got[0].PosterS3Key != nil {
		t.Errorf("poster_s3_key = %v, want nil", got[0].PosterS3Key)
	}
}

// Ownership and parent liveness are enforced in the repository, matching the
// photo contract: another user's id is indistinguishable from a missing one.
func TestVideoRepository_OwnershipAndParentLiveness(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	owner, other := "user-1", "user-2"
	activityID := insertTestActivity(t, dbc, owner)

	v, _ := repo.Insert(ctx, newVideo(owner, activityID))
	_ = repo.MarkReady(ctx, owner, activityID, v.ID, 10, nil)

	if _, err := repo.Get(ctx, other, activityID, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get as other user = %v, want ErrNotFound", err)
	}
	got, err := repo.ListByActivity(ctx, other, activityID)
	if err != nil {
		t.Fatalf("list as other: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("other user sees %d videos, want 0", len(got))
	}

	// Soft-deleting the PARENT hides the video without touching its row.
	if _, execErr := dbc.Exec(`UPDATE activities SET deleted_at = ? WHERE id = ?`, time.Now().UTC(), activityID); execErr != nil {
		t.Fatalf("soft-delete parent: %v", execErr)
	}
	got, err = repo.ListByActivity(ctx, owner, activityID)
	if err != nil {
		t.Fatalf("list after parent delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("videos visible under a soft-deleted parent: %d", len(got))
	}
}

// Reorder rewrites positions densely and LiveIDs reflects the new order.
func TestVideoRepository_ReorderAndLiveIDs(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	var ids []string
	for i := 0; i < 3; i++ {
		v, err := repo.Insert(ctx, newVideo(userID, activityID))
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if err := repo.MarkReady(ctx, userID, activityID, v.ID, int64(i+1), nil); err != nil {
			t.Fatalf("ready %d: %v", i, err)
		}
		ids = append(ids, v.ID)
	}

	live, err := repo.LiveIDs(ctx, userID, activityID)
	if err != nil {
		t.Fatalf("live ids: %v", err)
	}
	if len(live) != 3 {
		t.Fatalf("live ids = %d, want 3", len(live))
	}

	reversed := []string{ids[2], ids[1], ids[0]}
	if reErr := repo.Reorder(ctx, userID, activityID, reversed); reErr != nil {
		t.Fatalf("reorder: %v", reErr)
	}
	got, err := repo.ListByActivity(ctx, userID, activityID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i, want := range reversed {
		if got[i].ID != want {
			t.Errorf("position %d = %s, want %s", i, got[i].ID, want)
		}
		if got[i].Position != i {
			t.Errorf("row %d position = %d, want %d", i, got[i].Position, i)
		}
	}
}

// The reaper's query finds abandoned reservations and nothing else: not ready
// rows, not fresh pending ones.
func TestVideoRepository_ListStalePendingSelectsOnlyAbandoned(t *testing.T) {
	repo, dbc := newVideoRepo(t)
	ctx := context.Background()
	userID := "user-1"
	activityID := insertTestActivity(t, dbc, userID)

	// A committed video — never stale, whatever its age.
	ready, _ := repo.Insert(ctx, newVideo(userID, activityID))
	_ = repo.MarkReady(ctx, userID, activityID, ready.ID, 1, nil)

	// An abandoned reservation, backdated past the cutoff.
	stale, _ := repo.Insert(ctx, newVideo(userID, activityID))
	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := dbc.Exec(`UPDATE activity_video SET created_at = ? WHERE id = ?`, old, stale.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// A reservation still in flight.
	fresh, _ := repo.Insert(ctx, newVideo(userID, activityID))

	got, err := repo.ListStalePending(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stale count = %d, want 1", len(got))
	}
	if got[0].ID != stale.ID {
		t.Errorf("stale id = %s, want %s (ready=%s fresh=%s)", got[0].ID, stale.ID, ready.ID, fresh.ID)
	}

	if delErr := repo.SoftDeleteByID(ctx, stale.ID); delErr != nil {
		t.Fatalf("soft delete by id: %v", delErr)
	}
	got, err = repo.ListStalePending(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list stale after reap: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("reaped row still listed: %d", len(got))
	}
}
