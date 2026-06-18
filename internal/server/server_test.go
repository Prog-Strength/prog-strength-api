package server

import (
	"strings"
	"testing"

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
