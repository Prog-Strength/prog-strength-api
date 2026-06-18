package beta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/beta"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// adminCtxRouter mounts the beta handler with the request context pre-seeded
// to the given user ID (simulating RequireUser having run), WITHOUT the admin
// gate — so the handler logic itself can be exercised directly.
func adminCtxRouter(h *beta.Handler, userID string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := authctx.WithUserID(req.Context(), userID)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	h.Mount(r)
	return r
}

// seedAdminUser creates a user in the repo and returns its ID. The handler
// resolves this to an email for the added_by field.
func seedAdminUser(t *testing.T, repo *user.SQLiteRepository, email string) string {
	t.Helper()
	u := &user.User{
		Email:        email,
		DisplayName:  "Admin",
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
	}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func TestHandler_AddListRoundTrip(t *testing.T) {
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))
	adminID := seedAdminUser(t, userRepo, "admin@example.com")
	srv := adminCtxRouter(beta.NewHandler(betaRepo, userRepo), adminID)

	// POST add → 201.
	body, _ := json.Marshal(map[string]string{"email": "Tester@Example.com", "note": "vip"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/beta-emails", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// GET list → 200, contains the normalized email with added_by = admin email.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/beta-emails", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	var resp struct {
		Data struct {
			Emails []beta.AllowedEmail `json:"emails"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(resp.Data.Emails) != 1 {
		t.Fatalf("emails len = %d, want 1", len(resp.Data.Emails))
	}
	got := resp.Data.Emails[0]
	if got.Email != "tester@example.com" {
		t.Fatalf("email = %q, want normalized tester@example.com", got.Email)
	}
	if got.AddedBy == nil || *got.AddedBy != "admin@example.com" {
		t.Fatalf("added_by = %v, want admin@example.com", got.AddedBy)
	}
	if got.Note == nil || *got.Note != "vip" {
		t.Fatalf("note = %v, want vip", got.Note)
	}
}

func TestHandler_IdempotentReAdd(t *testing.T) {
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))
	adminID := seedAdminUser(t, userRepo, "admin@example.com")
	srv := adminCtxRouter(beta.NewHandler(betaRepo, userRepo), adminID)

	post := func() int {
		body, _ := json.Marshal(map[string]string{"email": "dup@example.com"})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/beta-emails", bytes.NewReader(body)))
		return rec.Code
	}

	if code := post(); code != http.StatusCreated {
		t.Fatalf("first POST = %d, want 201", code)
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("second POST = %d, want 200 (idempotent)", code)
	}

	emails, err := betaRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("list len = %d, want 1 (no dup)", len(emails))
	}
}

func TestHandler_DeleteThenDeleteAgain(t *testing.T) {
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))
	adminID := seedAdminUser(t, userRepo, "admin@example.com")
	srv := adminCtxRouter(beta.NewHandler(betaRepo, userRepo), adminID)

	if err := betaRepo.Add(context.Background(), "gone@example.com", "", ""); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	// DELETE with a URL-encoded path param.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/admin/beta-emails/gone%40example.com", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first DELETE = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/admin/beta-emails/gone%40example.com", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", rec.Code)
	}
}

func TestHandler_MalformedEmail400(t *testing.T) {
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))
	adminID := seedAdminUser(t, userRepo, "admin@example.com")
	srv := adminCtxRouter(beta.NewHandler(betaRepo, userRepo), adminID)

	// Empty email after normalization → 400.
	body, _ := json.Marshal(map[string]string{"email": "   "})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/beta-emails", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank email POST = %d, want 400", rec.Code)
	}

	// Invalid JSON → 400.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/beta-emails", bytes.NewReader([]byte("{not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON POST = %d, want 400", rec.Code)
	}
}

// TestHandler_NonAdmin403EveryVerb mounts the routes behind the real
// auth.RequireAdmin gate and asserts a non-admin user gets 403 on every verb.
func TestHandler_NonAdmin403EveryVerb(t *testing.T) {
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	userRepo := user.NewSQLiteRepository(dbtest.New(t))
	// Seed a NON-admin user (their email is not in adminEmails).
	nonAdminID := seedAdminUser(t, userRepo, "notadmin@example.com")

	r := chi.NewRouter()
	// Inject the user ID (as RequireUser would), then apply the real admin gate.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), nonAdminID)))
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin(userRepo, []string{"admin@example.com"}))
		beta.NewHandler(betaRepo, userRepo).Mount(r)
	})

	cases := []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/admin/beta-emails", nil},
		{http.MethodPost, "/admin/beta-emails", []byte(`{"email":"x@example.com"}`)},
		{http.MethodDelete, "/admin/beta-emails/x%40example.com", nil},
	}
	for _, tc := range cases {
		var rdr *bytes.Reader
		if tc.body != nil {
			rdr = bytes.NewReader(tc.body)
		} else {
			rdr = bytes.NewReader(nil)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, rdr))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}
