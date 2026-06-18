package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
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
func seedUser(t *testing.T, repo user.Repository, email string) string {
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

func TestRequireUser_BearerHeaderPasses(t *testing.T) {
	secret := []byte("test-secret")
	token, err := Sign("user-123", secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var reached bool
	h := RequireUser(secret)(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("valid bearer header was rejected, want pass")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// A top-level browser navigation (e.g. the Google Calendar connect redirect)
// can't attach an Authorization header, but it does carry the SameSite=Lax
// auth_token cookie set at login. RequireUser must accept that cookie as a
// fallback so those endpoints aren't unreachable from the browser.
func TestRequireUser_AuthCookieFallbackPasses(t *testing.T) {
	secret := []byte("test-secret")
	token, err := Sign("user-123", secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var reached bool
	h := RequireUser(secret)(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/connect", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("valid auth_token cookie was rejected, want pass")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// The Authorization header wins over the cookie when both are present, so a
// caller can always override a stale cookie by sending an explicit token.
func TestRequireUser_HeaderTakesPrecedenceOverCookie(t *testing.T) {
	secret := []byte("test-secret")
	headerToken, err := Sign("header-user", secret)
	if err != nil {
		t.Fatalf("sign header token: %v", err)
	}

	var gotUserID string
	h := RequireUser(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID, _ = UserIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+headerToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "garbage-cookie-value"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotUserID != "header-user" {
		t.Fatalf("user id = %q, want header-user (header should win)", gotUserID)
	}
}

func TestRequireUser_NoTokenRejected(t *testing.T) {
	var reached bool
	h := RequireUser([]byte("test-secret"))(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("request with no token reached handler, want 401")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAdmin_AdminPasses(t *testing.T) {
	repo := user.NewSQLiteRepository(dbtest.New(t))
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
	repo := user.NewSQLiteRepository(dbtest.New(t))
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
	repo := user.NewSQLiteRepository(dbtest.New(t))
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
	repo := user.NewSQLiteRepository(dbtest.New(t))

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
	repo := user.NewSQLiteRepository(dbtest.New(t))

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
