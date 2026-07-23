package server

import (
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
)

// TestNew_RequiresDatabaseURL locks in the fail-fast guard: with no
// DatabaseURL configured, New must refuse to build a server and return an
// actionable error mentioning DATABASE_URL. The in-memory repositories were
// removed, so a SQLite file path is now mandatory.
//
// New runs no required-field guards before the DATABASE_URL check (the only
// earlier returns are conditional on TCX_BUCKET_NAME / AvatarBucketName being
// set), so an empty config reaches the DATABASE_URL guard directly. We clear
// TCX_BUCKET_NAME to keep that true regardless of the ambient environment.
func TestNew_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("TCX_BUCKET_NAME", "")

	srv, err := New(config.Config{DatabaseURL: ""})

	if err == nil {
		t.Fatal("New(config.Config{}) returned nil error; expected a DATABASE_URL guard error")
	}
	if srv != nil {
		t.Fatalf("New(config.Config{}) returned non-nil *Server (%v); expected nil on error", srv)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("error %q does not mention DATABASE_URL; the message must stay actionable", err.Error())
	}
}

// TestNew_MountsWhoopRoutesWhenConfigured locks in the Task 10 wiring: with the
// four WHOOP_* values set (client id/secret, redirect URL, and a valid 32-byte
// AES key), New mounts the public callback + webhook and the authed connect/
// connection/recovery routes. We walk the resulting chi router and assert the
// key routes are present. This is the whoop analog of the calendar sync
// mounting the task references.
func TestNew_MountsWhoopRoutesWhenConfigured(t *testing.T) {
	// Isolate from any ambient env that would toggle other optional features.
	t.Setenv("TCX_BUCKET_NAME", "")
	t.Setenv("AVATAR_BUCKET_NAME", "")

	encKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	srv, err := New(config.Config{
		DatabaseURL:       filepath.Join(t.TempDir(), "app.db"),
		JWTSigningKey:     "test-secret",
		WhoopClientID:     "whoop-client",
		WhoopClientSecret: "whoop-secret",
		WhoopRedirectURL:  "https://api.example.com/auth/whoop/callback",
		WhoopTokenEncKey:  encKey,
	})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}

	router, ok := srv.httpServer.Handler.(chi.Router)
	if !ok {
		t.Fatal("server handler is not a chi.Router")
	}

	routes := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	want := []string{
		"GET /auth/whoop/callback", // public
		"POST /webhooks/whoop",     // public webhook
		"GET /auth/whoop/connect",  // authed
		"GET /me/whoop/connection", // authed
		"GET /whoop/recovery",      // authed
	}
	for _, r := range want {
		if !routes[r] {
			t.Errorf("expected route %q to be mounted; got routes %v", r, routes)
		}
	}
}
