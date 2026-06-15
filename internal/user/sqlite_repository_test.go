package user

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newSQLiteUserRepo spins up a migrated, file-backed SQLite database and
// returns a user repository over it. db.Open appends its own connection
// params, so a bare temp-dir path is passed.
func newSQLiteUserRepo(t *testing.T) (*SQLiteRepository, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "user.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewSQLiteRepository(sqlDB), sqlDB
}

func strPtr(s string) *string { return &s }

// TestSQLite_CreateRoundTripsProfileColumns checks that all three new columns
// (height_cm, avatar_key, oauth_avatar_url) persist and scan back on create.
func TestSQLite_CreateRoundTripsProfileColumns(t *testing.T) {
	repo, _ := newSQLiteUserRepo(t)
	ctx := context.Background()

	u := &User{
		Email:          "lifter@example.com",
		DisplayName:    "Lifter",
		WeightUnit:     WeightUnitPounds,
		DistanceUnit:   DistanceUnitMiles,
		HeightCm:       floatPtr(180),
		AvatarKey:      strPtr("user_id=x/abc.png"),
		OAuthAvatarURL: strPtr("https://oauth.example/a.png"),
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.HeightCm == nil || *got.HeightCm != 180 {
		t.Fatalf("height_cm: got %v want 180", got.HeightCm)
	}
	if got.AvatarKey == nil || *got.AvatarKey != "user_id=x/abc.png" {
		t.Fatalf("avatar_key: got %v", got.AvatarKey)
	}
	if got.OAuthAvatarURL == nil || *got.OAuthAvatarURL != "https://oauth.example/a.png" {
		t.Fatalf("oauth_avatar_url: got %v", got.OAuthAvatarURL)
	}
}

// TestSQLite_CreateNullProfileColumns checks NULLs scan back as nil pointers.
func TestSQLite_CreateNullProfileColumns(t *testing.T) {
	repo, _ := newSQLiteUserRepo(t)
	ctx := context.Background()

	u := &User{
		Email:        "lifter@example.com",
		DisplayName:  "Lifter",
		WeightUnit:   WeightUnitPounds,
		DistanceUnit: DistanceUnitMiles,
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.HeightCm != nil {
		t.Fatalf("height_cm: got %v want nil", *got.HeightCm)
	}
	if got.AvatarKey != nil {
		t.Fatalf("avatar_key: got %v want nil", *got.AvatarKey)
	}
	if got.OAuthAvatarURL != nil {
		t.Fatalf("oauth_avatar_url: got %v want nil", *got.OAuthAvatarURL)
	}
}

// TestSQLite_UpdateProfileColumns checks the new columns persist through Update
// in both directions (set then clear).
func TestSQLite_UpdateProfileColumns(t *testing.T) {
	repo, _ := newSQLiteUserRepo(t)
	ctx := context.Background()

	u := &User{
		Email:        "lifter@example.com",
		DisplayName:  "Lifter",
		WeightUnit:   WeightUnitPounds,
		DistanceUnit: DistanceUnitMiles,
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Set height + avatar_key.
	u.HeightCm = floatPtr(175)
	u.AvatarKey = strPtr("user_id=x/new.webp")
	u.OAuthAvatarURL = strPtr("https://oauth.example/b.png")
	if err := repo.Update(ctx, u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.HeightCm == nil || *got.HeightCm != 175 {
		t.Fatalf("height_cm after set: got %v want 175", got.HeightCm)
	}
	if got.AvatarKey == nil || *got.AvatarKey != "user_id=x/new.webp" {
		t.Fatalf("avatar_key after set: got %v", got.AvatarKey)
	}

	// Clear avatar_key (NULL round-trip through Update).
	got.AvatarKey = nil
	if clearErr := repo.Update(ctx, got); clearErr != nil {
		t.Fatalf("Update clear: %v", clearErr)
	}
	cleared, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cleared.AvatarKey != nil {
		t.Fatalf("avatar_key after clear: got %v want nil", *cleared.AvatarKey)
	}
	// Height untouched by the clear update.
	if cleared.HeightCm == nil || *cleared.HeightCm != 175 {
		t.Fatalf("height_cm should persist: got %v", cleared.HeightCm)
	}
}

// TestSQLite_TimezoneAndCalendarDetailDefaultsAndUpdate checks the two new
// columns default to UTC/time_block on create and round-trip through Update.
func TestSQLite_TimezoneAndCalendarDetailDefaultsAndUpdate(t *testing.T) {
	repo, _ := newSQLiteUserRepo(t)
	ctx := context.Background()

	u := &User{
		Email:        "lifter@example.com",
		DisplayName:  "Lifter",
		WeightUnit:   WeightUnitPounds,
		DistanceUnit: DistanceUnitMiles,
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Timezone != "UTC" {
		t.Fatalf("timezone default: got %q want UTC", got.Timezone)
	}
	if got.CalendarDefaultDetail != "time_block" {
		t.Fatalf("calendar_default_detail default: got %q want time_block", got.CalendarDefaultDetail)
	}

	got.Timezone = "America/New_York"
	got.CalendarDefaultDetail = "full_agenda"
	if err = repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Timezone != "America/New_York" {
		t.Fatalf("timezone after update: got %q want America/New_York", after.Timezone)
	}
	if after.CalendarDefaultDetail != "full_agenda" {
		t.Fatalf("calendar_default_detail after update: got %q want full_agenda", after.CalendarDefaultDetail)
	}
}
