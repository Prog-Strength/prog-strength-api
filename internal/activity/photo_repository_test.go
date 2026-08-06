package activity

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// newPhotoRepo returns a photo repository backed by a fresh migrated DB,
// sharing the same *sql.DB so the test can insert parent activities directly.
func newPhotoRepo(t *testing.T) (*SQLitePhotoRepository, *sql.DB) {
	t.Helper()
	dbc := newMigratedDB(t)
	return NewSQLitePhotoRepository(dbc), dbc
}

// insertTestActivity inserts a minimal live activities row owned by userID and
// returns its id. Reuses CreateManual's write path so the schema stays honest.
func insertTestActivity(t *testing.T, dbc *sql.DB, userID string) string {
	t.Helper()
	repo := NewSQLiteRepository(dbc, NewMemoryArchiver())
	activityID, err := repo.CreateManual(context.Background(), userID, CreateRequest{
		Type:      ActivityStrengthTraining,
		StartTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("insert test activity: %v", err)
	}
	return activityID
}

// newPhoto builds a minimal valid ActivityPhoto for the given owner/activity.
func newPhoto(userID, activityID string) ActivityPhoto {
	return ActivityPhoto{
		ActivityID:  activityID,
		UserID:      userID,
		S3Key:       "full/" + id.New() + ".jpg",
		ThumbS3Key:  "thumb/" + id.New() + ".jpg",
		ContentType: "image/jpeg",
		ByteSize:    12345,
		Width:       800,
		Height:      600,
	}
}

func mustInsertPhoto(t *testing.T, r *SQLitePhotoRepository, userID, activityID string) ActivityPhoto {
	t.Helper()
	p, err := r.Insert(context.Background(), newPhoto(userID, activityID))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return p
}

func softDeleteParent(t *testing.T, dbc *sql.DB, activityID string) {
	t.Helper()
	if _, err := dbc.Exec(`UPDATE activities SET deleted_at = ? WHERE id = ?`, time.Now().UTC(), activityID); err != nil {
		t.Fatalf("soft delete parent: %v", err)
	}
}

func restoreParent(t *testing.T, dbc *sql.DB, activityID string) {
	t.Helper()
	if _, err := dbc.Exec(`UPDATE activities SET deleted_at = NULL WHERE id = ?`, activityID); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
}

func TestPhotoInsert_AssignsCompactPositionsPerActivity(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a1 := insertTestActivity(t, dbc, "u1")
	a2 := insertTestActivity(t, dbc, "u1")

	for i := 0; i < 3; i++ {
		p, err := r.Insert(ctx, newPhoto("u1", a1))
		if err != nil {
			t.Fatalf("Insert a1 #%d: %v", i, err)
		}
		if p.Position != i {
			t.Fatalf("a1 photo #%d: position = %d, want %d", i, p.Position, i)
		}
		if p.ID == "" {
			t.Fatal("expected generated photo ID")
		}
		if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
			t.Fatal("expected timestamps set on stored row")
		}
	}

	// A separate activity gets an independent position sequence starting at 0.
	p2, err := r.Insert(ctx, newPhoto("u1", a2))
	if err != nil {
		t.Fatalf("Insert a2: %v", err)
	}
	if p2.Position != 0 {
		t.Fatalf("a2 first photo: position = %d, want 0", p2.Position)
	}
}

func TestPhotoListByActivity_LiveOrderedParentLive(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")

	p0 := mustInsertPhoto(t, r, "u1", a)
	p1 := mustInsertPhoto(t, r, "u1", a)
	p2 := mustInsertPhoto(t, r, "u1", a)

	got, err := r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{p0.ID, p1.ID, p2.ID}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Fatalf("order[%d] = %s, want %s", i, got[i].ID, w)
		}
		if got[i].Position != i {
			t.Fatalf("photo %d position = %d, want %d", i, got[i].Position, i)
		}
	}

	// Soft-deleting one photo removes it from the list.
	if delErr := r.SoftDelete(ctx, "u1", a, p1.ID); delErr != nil {
		t.Fatalf("SoftDelete: %v", delErr)
	}
	got, err = r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity after delete: %v", err)
	}
	if len(got) != 2 || got[0].ID != p0.ID || got[1].ID != p2.ID {
		t.Fatalf("after soft-delete got %+v, want [p0 p2]", got)
	}

	// Soft-deleting the parent activity hides ALL its photos.
	softDeleteParent(t, dbc, a)
	got, err = r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity deleted parent: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("deleted parent should hide photos, got %d", len(got))
	}

	// Restoring the parent brings the live photos back.
	restoreParent(t, dbc, a)
	got, err = r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity restored parent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("restored parent should show live photos, got %d", len(got))
	}
}

func TestPhotoCountLive(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")

	if n, err := r.CountLive(ctx, a); err != nil || n != 0 {
		t.Fatalf("CountLive empty = %d, %v; want 0", n, err)
	}
	mustInsertPhoto(t, r, "u1", a)
	p := mustInsertPhoto(t, r, "u1", a)
	mustInsertPhoto(t, r, "u1", a)
	if n, err := r.CountLive(ctx, a); err != nil || n != 3 {
		t.Fatalf("CountLive = %d, %v; want 3", n, err)
	}
	if err := r.SoftDelete(ctx, "u1", a, p.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if n, err := r.CountLive(ctx, a); err != nil || n != 2 {
		t.Fatalf("CountLive after delete = %d, %v; want 2", n, err)
	}
}

func TestPhotoGet(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")
	p := mustInsertPhoto(t, r, "u1", a)

	got, err := r.Get(ctx, "u1", a, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != p.ID || got.S3Key != p.S3Key {
		t.Fatalf("Get returned %+v, want %+v", got, p)
	}

	// Another user cannot read it.
	if _, err := r.Get(ctx, "u2", a, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get other user err = %v, want ErrNotFound", err)
	}

	// A soft-deleted photo is not found.
	if err := r.SoftDelete(ctx, "u1", a, p.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := r.Get(ctx, "u1", a, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted photo err = %v, want ErrNotFound", err)
	}

	// A live photo under a soft-deleted parent is not found.
	p2 := mustInsertPhoto(t, r, "u1", a)
	softDeleteParent(t, dbc, a)
	if _, err := r.Get(ctx, "u1", a, p2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get under deleted parent err = %v, want ErrNotFound", err)
	}
}

func TestPhotoUpdateCaption(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")
	p := mustInsertPhoto(t, r, "u1", a)

	caption := "at the summit"
	if err := r.UpdateCaption(ctx, "u1", a, p.ID, &caption); err != nil {
		t.Fatalf("UpdateCaption set: %v", err)
	}
	got, err := r.Get(ctx, "u1", a, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Caption == nil || *got.Caption != caption {
		t.Fatalf("caption = %v, want %q", got.Caption, caption)
	}
	if !got.UpdatedAt.After(p.UpdatedAt) && !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("updated_at not advanced: %v vs %v", got.UpdatedAt, p.UpdatedAt)
	}

	// Clearing the caption stores NULL.
	if clearErr := r.UpdateCaption(ctx, "u1", a, p.ID, nil); clearErr != nil {
		t.Fatalf("UpdateCaption clear: %v", clearErr)
	}
	got, err = r.Get(ctx, "u1", a, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Caption != nil {
		t.Fatalf("caption = %v, want nil after clear", got.Caption)
	}

	// Ownership enforced.
	if err := r.UpdateCaption(ctx, "u2", a, p.ID, &caption); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateCaption other user err = %v, want ErrNotFound", err)
	}
}

func TestPhotoSoftDelete(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")
	p := mustInsertPhoto(t, r, "u1", a)

	if err := r.SoftDelete(ctx, "u1", a, p.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	got, err := r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no live photos, got %d", len(got))
	}

	// Deleting again / other user is ErrNotFound.
	if err := r.SoftDelete(ctx, "u1", a, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double SoftDelete err = %v, want ErrNotFound", err)
	}
}

func TestPhotoReorderAndLiveIDs(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")
	p0 := mustInsertPhoto(t, r, "u1", a)
	p1 := mustInsertPhoto(t, r, "u1", a)
	p2 := mustInsertPhoto(t, r, "u1", a)

	ids, err := r.LiveIDs(ctx, "u1", a)
	if err != nil {
		t.Fatalf("LiveIDs: %v", err)
	}
	if len(ids) != 3 || ids[0] != p0.ID || ids[1] != p1.ID || ids[2] != p2.ID {
		t.Fatalf("LiveIDs = %v, want ordered [p0 p1 p2]", ids)
	}

	// Reverse the order.
	newOrder := []string{p2.ID, p1.ID, p0.ID}
	if reErr := r.Reorder(ctx, "u1", a, newOrder); reErr != nil {
		t.Fatalf("Reorder: %v", reErr)
	}
	got, err := r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity: %v", err)
	}
	for i, w := range newOrder {
		if got[i].ID != w {
			t.Fatalf("after reorder order[%d] = %s, want %s", i, got[i].ID, w)
		}
		if got[i].Position != i {
			t.Fatalf("after reorder position[%d] = %d, want %d", i, got[i].Position, i)
		}
	}

	// Idempotent: reorder to the current order is a no-op.
	if reErr := r.Reorder(ctx, "u1", a, newOrder); reErr != nil {
		t.Fatalf("Reorder idempotent: %v", reErr)
	}
	got2, err := r.ListByActivity(ctx, "u1", a)
	if err != nil {
		t.Fatalf("ListByActivity: %v", err)
	}
	for i, w := range newOrder {
		if got2[i].ID != w || got2[i].Position != i {
			t.Fatalf("idempotent reorder drifted at %d: %+v", i, got2[i])
		}
	}
}

func TestPhotoCoverPhotosByActivityIDs(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()

	// a1 owned by u1: 3 photos, cover is the position-0 photo.
	a1 := insertTestActivity(t, dbc, "u1")
	cover1 := mustInsertPhoto(t, r, "u1", a1)
	mustInsertPhoto(t, r, "u1", a1)
	mustInsertPhoto(t, r, "u1", a1)

	// a2 owned by u2 (timeline spans authors): 1 photo.
	a2 := insertTestActivity(t, dbc, "u2")
	cover2 := mustInsertPhoto(t, r, "u2", a2)

	// a3: no photos -> absent from map.
	a3 := insertTestActivity(t, dbc, "u1")

	// a4: has a photo but parent is soft-deleted -> absent from map.
	a4 := insertTestActivity(t, dbc, "u1")
	mustInsertPhoto(t, r, "u1", a4)
	softDeleteParent(t, dbc, a4)

	covers, err := r.CoverPhotosByActivityIDs(ctx, []string{a1, a2, a3, a4})
	if err != nil {
		t.Fatalf("CoverPhotosByActivityIDs: %v", err)
	}
	if len(covers) != 2 {
		t.Fatalf("len(covers) = %d, want 2 (a1,a2)", len(covers))
	}
	c1, ok := covers[a1]
	if !ok {
		t.Fatal("a1 missing from covers")
	}
	if c1.Cover.ID != cover1.ID {
		t.Fatalf("a1 cover = %s, want %s (lowest position)", c1.Cover.ID, cover1.ID)
	}
	if c1.Count != 3 {
		t.Fatalf("a1 count = %d, want 3", c1.Count)
	}
	c2, ok := covers[a2]
	if !ok {
		t.Fatal("a2 missing from covers")
	}
	if c2.Cover.ID != cover2.ID || c2.Count != 1 {
		t.Fatalf("a2 cover/count = %s/%d, want %s/1", c2.Cover.ID, c2.Count, cover2.ID)
	}
	if _, ok := covers[a3]; ok {
		t.Fatal("a3 (no photos) should be absent")
	}
	if _, ok := covers[a4]; ok {
		t.Fatal("a4 (deleted parent) should be absent")
	}

	// Empty input returns an empty map, no query.
	empty, err := r.CoverPhotosByActivityIDs(ctx, nil)
	if err != nil {
		t.Fatalf("CoverPhotosByActivityIDs empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty input returned %d entries", len(empty))
	}
}

func TestPhotoHardDeleteCascade(t *testing.T) {
	t.Parallel()
	r, dbc := newPhotoRepo(t)
	ctx := context.Background()
	a := insertTestActivity(t, dbc, "u1")
	mustInsertPhoto(t, r, "u1", a)
	mustInsertPhoto(t, r, "u1", a)

	if _, err := dbc.ExecContext(ctx, `DELETE FROM activities WHERE id = ?`, a); err != nil {
		t.Fatalf("hard delete activity: %v", err)
	}

	var n int
	if err := dbc.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_photo WHERE activity_id = ?`, a).Scan(&n); err != nil {
		t.Fatalf("count photos: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected FK cascade to remove photos, got %d rows", n)
	}
}
