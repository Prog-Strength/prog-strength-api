package auth

import (
	"context"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/beta"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// newFindOrCreateHandler builds a minimal Handler backed by an in-memory user
// repository, sufficient to exercise findOrCreateUser (no Google config or JWT
// signing involved in that path).
func newFindOrCreateHandler() (*Handler, *user.MemoryRepository) {
	repo := user.NewMemoryRepository()
	h := NewHandler(Config{JWTSecret: []byte("test-secret")}, repo, beta.NewMemoryRepository())
	return h, repo
}

// TestFindOrCreate_NewUserPersistsAvatar verifies a brand-new Google user has
// the picture URL stored as oauth_avatar_url.
func TestFindOrCreate_NewUserPersistsAvatar(t *testing.T) {
	h, repo := newFindOrCreateHandler()
	ctx := context.Background()

	u, err := h.findOrCreateUser(ctx, "new@example.com", "New", "https://pic.example/a.png")
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if u.OAuthAvatarURL == nil || *u.OAuthAvatarURL != "https://pic.example/a.png" {
		t.Fatalf("oauth_avatar_url on create: got %v", u.OAuthAvatarURL)
	}
	// Persisted (not just on the returned struct).
	stored, err := repo.GetByEmail(ctx, "new@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if stored.OAuthAvatarURL == nil || *stored.OAuthAvatarURL != "https://pic.example/a.png" {
		t.Fatalf("oauth_avatar_url not persisted: got %v", stored.OAuthAvatarURL)
	}
}

// TestFindOrCreate_ExistingUserRefreshesChangedAvatar verifies an existing user
// with a different picture URL gets it opportunistically refreshed on login.
func TestFindOrCreate_ExistingUserRefreshesChangedAvatar(t *testing.T) {
	h, repo := newFindOrCreateHandler()
	ctx := context.Background()

	if _, err := h.findOrCreateUser(ctx, "u@example.com", "U", "https://pic.example/old.png"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := h.findOrCreateUser(ctx, "u@example.com", "U", "https://pic.example/new.png")
	if err != nil {
		t.Fatalf("login refresh: %v", err)
	}
	if got.OAuthAvatarURL == nil || *got.OAuthAvatarURL != "https://pic.example/new.png" {
		t.Fatalf("oauth_avatar_url not refreshed: got %v", got.OAuthAvatarURL)
	}
	stored, _ := repo.GetByEmail(ctx, "u@example.com")
	if stored.OAuthAvatarURL == nil || *stored.OAuthAvatarURL != "https://pic.example/new.png" {
		t.Fatalf("refresh not persisted: got %v", stored.OAuthAvatarURL)
	}
}

// TestFindOrCreate_EmptyPictureLeavesNil verifies an empty picture on create
// leaves oauth_avatar_url nil, and an empty picture on an existing login does
// not overwrite a stored value.
func TestFindOrCreate_EmptyPictureLeavesNil(t *testing.T) {
	h, _ := newFindOrCreateHandler()
	ctx := context.Background()

	u, err := h.findOrCreateUser(ctx, "np@example.com", "NP", "")
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if u.OAuthAvatarURL != nil {
		t.Fatalf("oauth_avatar_url should be nil with empty picture: got %v", *u.OAuthAvatarURL)
	}

	// Existing user with a stored avatar; an empty picture must not clear it.
	if _, seedErr := h.findOrCreateUser(ctx, "keep@example.com", "K", "https://pic.example/keep.png"); seedErr != nil {
		t.Fatalf("seed: %v", seedErr)
	}
	got, err := h.findOrCreateUser(ctx, "keep@example.com", "K", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got.OAuthAvatarURL == nil || *got.OAuthAvatarURL != "https://pic.example/keep.png" {
		t.Fatalf("empty picture overwrote stored avatar: got %v", got.OAuthAvatarURL)
	}
}

// TestFindOrCreate_NewUserGetsValidHandle verifies a brand-new user comes back
// with a non-nil, valid, persisted handle auto-assigned from their display name.
func TestFindOrCreate_NewUserGetsValidHandle(t *testing.T) {
	h, repo := newFindOrCreateHandler()
	ctx := context.Background()

	u, err := h.findOrCreateUser(ctx, "sam@example.com", "Sam Lifter", "")
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if u.Username == nil {
		t.Fatal("want non-nil username on a new user, got nil")
	}
	if _, vErr := user.ValidateUsername(*u.Username); vErr != nil {
		t.Fatalf("assigned handle %q is invalid: %v", *u.Username, vErr)
	}
	// Persisted, not just on the returned struct.
	stored, err := repo.GetByEmail(ctx, "sam@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if stored.Username == nil || *stored.Username != *u.Username {
		t.Fatalf("handle not persisted: returned %v, stored %v", u.Username, stored.Username)
	}
}

// TestFindOrCreate_TwoUsersGetDistinctHandles verifies two new users whose
// display names slugify to the same base receive DISTINCT valid handles (the
// uniqueness probe sees the first user's handle when generating the second).
func TestFindOrCreate_TwoUsersGetDistinctHandles(t *testing.T) {
	h, _ := newFindOrCreateHandler()
	ctx := context.Background()

	a, err := h.findOrCreateUser(ctx, "sam1@example.com", "Sam Lifter", "")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := h.findOrCreateUser(ctx, "sam2@example.com", "Sam Lifter", "")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.Username == nil || b.Username == nil {
		t.Fatalf("want both handles non-nil, got a=%v b=%v", a.Username, b.Username)
	}
	if *a.Username == *b.Username {
		t.Fatalf("want distinct handles, both got %q", *a.Username)
	}
}

// TestFindOrCreate_DevTokenPathEmptyPicture verifies the dev-token style call
// (empty avatarURL) succeeds without error and leaves oauth_avatar_url nil.
func TestFindOrCreate_DevTokenPathEmptyPicture(t *testing.T) {
	h, _ := newFindOrCreateHandler()
	ctx := context.Background()

	u, err := h.findOrCreateUser(ctx, "dev@example.com", "dev@example.com", "")
	if err != nil {
		t.Fatalf("dev-token find or create: %v", err)
	}
	if u.OAuthAvatarURL != nil {
		t.Fatalf("dev path should leave oauth_avatar_url nil: got %v", *u.OAuthAvatarURL)
	}
}
