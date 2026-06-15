package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// okHandler is a sentinel next-handler that records whether it was reached.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// seedUser creates a user and returns its ID.
func seedUser(t *testing.T, repo *user.MemoryRepository, email string) string {
	t.Helper()
	u := &user.User{
		Email:        email,
		DisplayName:  "U",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
	}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func TestRequireAdmin_AdminPasses(t *testing.T) {
	repo := user.NewMemoryRepository()
	id := seedUser(t, repo, "Admin@Example.com")

	var reached bool
	mw := RequireAdmin(repo, []string{"admin@example.com"})
	h := mw(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	req = req.WithContext(WithUserID(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("admin was blocked, want pass")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRequireAdmin_NonAdminForbidden(t *testing.T) {
	repo := user.NewMemoryRepository()
	id := seedUser(t, repo, "user@example.com")

	var reached bool
	h := RequireAdmin(repo, []string{"admin@example.com"})(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	req = req.WithContext(WithUserID(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("non-admin reached handler, want 403")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRequireAdmin_EmptyAdminListDeniesEveryone(t *testing.T) {
	repo := user.NewMemoryRepository()
	id := seedUser(t, repo, "admin@example.com")

	var reached bool
	h := RequireAdmin(repo, nil)(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	req = req.WithContext(WithUserID(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("empty admin list let a request through, want fail-closed 403")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRequireAdmin_NoUserInContextDenied(t *testing.T) {
	repo := user.NewMemoryRepository()

	var reached bool
	h := RequireAdmin(repo, []string{"admin@example.com"})(okHandler(&reached))

	// No WithUserID on the context.
	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("missing user in context reached handler, want 403")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRequireAdmin_UnresolvableUserDenied(t *testing.T) {
	repo := user.NewMemoryRepository()

	var reached bool
	h := RequireAdmin(repo, []string{"admin@example.com"})(okHandler(&reached))

	// A user ID that doesn't exist in the repo.
	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	req = req.WithContext(WithUserID(req.Context(), "nonexistent-id"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("unresolvable user reached handler, want 403")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
