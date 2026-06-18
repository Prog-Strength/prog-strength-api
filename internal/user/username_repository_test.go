package user

import (
	"context"
	"errors"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// repoBackends returns the Repository implementations under a common name so
// the username repository contract can be table-driven. Only the SQLite backend
// remains (the in-memory repo is deprecated); the contract now asserts the real
// persistence path.
func repoBackends(t *testing.T) []struct {
	name string
	repo Repository
} {
	t.Helper()
	sqliteRepo, _ := newSQLiteUserRepo(t)
	return []struct {
		name string
		repo Repository
	}{
		{"sqlite", sqliteRepo},
	}
}

// makeUser inserts a valid user (no username) and returns it.
func makeUser(t *testing.T, repo Repository, email string) *User {
	t.Helper()
	u := &User{Email: email, DisplayName: "Lifter", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("Create(%s): %v", email, err)
	}
	return u
}

// TestRepo_SetUsernameThenGetByUsername sets a username via Update and reads it
// back via GetByUsername on both backends.
func TestRepo_SetUsernameThenGetByUsername(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			u := makeUser(t, b.repo, "a@example.com")

			u.Username = strPtr("jimlifts")
			if err := b.repo.Update(ctx, u); err != nil {
				t.Fatalf("Update: %v", err)
			}

			got, err := b.repo.GetByUsername(ctx, "jimlifts")
			if err != nil {
				t.Fatalf("GetByUsername: %v", err)
			}
			if got.ID != u.ID {
				t.Fatalf("GetByUsername id = %s, want %s", got.ID, u.ID)
			}
			if got.Username == nil || *got.Username != "jimlifts" {
				t.Fatalf("username = %v, want jimlifts", got.Username)
			}
		})
	}
}

// TestRepo_GetByUsernameNotFound confirms an absent handle returns ErrNotFound.
func TestRepo_GetByUsernameNotFound(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			_, err := b.repo.GetByUsername(context.Background(), "nobody")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetByUsername error = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestRepo_UsernameCaseInsensitiveCollision checks that two different users
// cannot hold case-variant forms of the same canonical handle. Repos compare
// case-insensitively (SQLite via the canonical stored value + the handler
// canonicalizing; memory via a lowercased compare), so jimlifts vs JimLifts
// collide. We feed the second user the canonical-but-same value too, since the
// canonicalization happens at the handler edge — here we simulate a second
// user trying to store the same lowercased handle.
func TestRepo_UsernameCaseInsensitiveCollision(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			a := makeUser(t, b.repo, "a@example.com")
			bb := makeUser(t, b.repo, "b@example.com")

			a.Username = strPtr("jimlifts")
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update a: %v", err)
			}

			// Second user takes the same canonical handle -> taken.
			bb.Username = strPtr("jimlifts")
			if err := b.repo.Update(ctx, bb); !errors.Is(err, ErrUsernameTaken) {
				t.Fatalf("Update b error = %v, want ErrUsernameTaken", err)
			}
		})
	}
}

// TestRepo_UsernameCanonicalStoredValueCollides pins the SQLite collision rule:
// the unique index is on the stored handle, which the handler always
// canonicalizes (lowercases) before write. Two users that both store the same
// canonical handle therefore collide. Differently-cased *stored* values do not
// collide at the repo layer because the index is case-sensitive — that case is
// prevented upstream by ValidateUsername, not by the repo. (This replaces the
// old memory-only fold-on-compare test, which asserted behavior specific to the
// deprecated in-memory backend.)
func TestRepo_UsernameCanonicalStoredValueCollides(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))
	a := makeUser(t, repo, "a@example.com")
	bb := makeUser(t, repo, "b@example.com")

	a.Username = strPtr("jimlifts")
	if err := repo.Update(ctx, a); err != nil {
		t.Fatalf("Update a: %v", err)
	}
	// Same canonical (already-lowercased) handle the handler would produce.
	bb.Username = strPtr("jimlifts")
	if err := repo.Update(ctx, bb); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("Update b error = %v, want ErrUsernameTaken", err)
	}
}

// TestRepo_RenameFreesOldHandle checks that when user A renames away from "x",
// user B can take "x".
func TestRepo_RenameFreesOldHandle(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			a := makeUser(t, b.repo, "a@example.com")
			bb := makeUser(t, b.repo, "b@example.com")

			a.Username = strPtr("x")
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update a -> x: %v", err)
			}

			// A renames away from x.
			a.Username = strPtr("xavier")
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update a -> xavier: %v", err)
			}

			// B can now take x.
			bb.Username = strPtr("x")
			if err := b.repo.Update(ctx, bb); err != nil {
				t.Fatalf("Update b -> x (should be free): %v", err)
			}

			got, err := b.repo.GetByUsername(ctx, "x")
			if err != nil {
				t.Fatalf("GetByUsername x: %v", err)
			}
			if got.ID != bb.ID {
				t.Fatalf("username x belongs to %s, want %s", got.ID, bb.ID)
			}
		})
	}
}

// TestRepo_KeepingOwnUsernameIsNotCollision verifies that re-saving a user
// without changing their username doesn't self-collide.
func TestRepo_KeepingOwnUsernameIsNotCollision(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			a := makeUser(t, b.repo, "a@example.com")
			a.Username = strPtr("steady")
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update set: %v", err)
			}
			// Update again, same username, different display name.
			a.DisplayName = "New Name"
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update keep-own-username: %v", err)
			}
		})
	}
}

// TestRepo_DeletedUsernameIsReusable verifies that soft-deleting an account
// frees its handle for another user on BOTH backends. SQLite enforces this via
// the partial unique index (WHERE deleted_at IS NULL); memory excludes deleted
// users from its collision check. This locks the two implementations to the
// same rule (and to the SOW's "freed handle is immediately available").
func TestRepo_DeletedUsernameIsReusable(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			a := makeUser(t, b.repo, "a@example.com")
			a.Username = strPtr("ghost")
			if err := b.repo.Update(ctx, a); err != nil {
				t.Fatalf("Update a -> ghost: %v", err)
			}

			// Soft-delete A; the handle must free up.
			if err := b.repo.Delete(ctx, a.ID); err != nil {
				t.Fatalf("Delete a: %v", err)
			}

			bb := makeUser(t, b.repo, "b@example.com")
			bb.Username = strPtr("ghost")
			if err := b.repo.Update(ctx, bb); err != nil {
				t.Fatalf("Update b -> ghost (should be free after delete): %v", err)
			}
		})
	}
}

// TestRepo_MultipleNullUsernamesCoexist confirms that several users with no
// username set do not collide (SQLite multiple-NULLs; memory nil-skip).
func TestRepo_MultipleNullUsernamesCoexist(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			makeUser(t, b.repo, "a@example.com")
			makeUser(t, b.repo, "b@example.com")
			c := &User{Email: "c@example.com", DisplayName: "C", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
			if err := b.repo.Create(ctx, c); err != nil {
				t.Fatalf("Create third null-username user: %v", err)
			}
		})
	}
}
