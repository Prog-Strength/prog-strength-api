package auth

import (
	"context"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/beta"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// The OAuth gate decision is the externally observable contract: googleCallback
// mints a token only when h.betaChecker.IsAllowed reports true (allowed →
// token path; not allowed → #error=beta_required redirect / 403). Standing up
// a full Google OAuth callback in a unit test requires a fake token endpoint
// and userinfo server; the SOW's intent is to prove the *dynamic* (no-restart)
// path, so these tests exercise the exact decision boundary the callback
// branches on — h.betaChecker.IsAllowed — including that the decision flips
// after an admin Add against the live in-memory repo, with no handler rebuild.

func newGateHandler(t *testing.T, betaRepo beta.Repository) *Handler {
	return NewHandler(Config{JWTSecret: []byte("test-secret")}, user.NewSQLiteRepository(dbtest.New(t)), betaRepo)
}

func TestGate_EmptyRepoAllowsEveryone(t *testing.T) {
	ctx := context.Background()
	h := newGateHandler(t, beta.NewSQLiteRepository(dbtest.New(t)))

	allowed, err := h.betaChecker.IsAllowed(ctx, "anyone@example.com")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("empty beta repo must allow everyone (gate disabled), got blocked")
	}
}

func TestGate_NonEmptyRepoBlocksAbsentEmail(t *testing.T) {
	ctx := context.Background()
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	if err := betaRepo.Add(ctx, "allowed@example.com", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	h := newGateHandler(t, betaRepo)

	allowed, err := h.betaChecker.IsAllowed(ctx, "absent@example.com")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if allowed {
		t.Fatal("absent email allowed past a non-empty gate, want blocked (→ beta_required)")
	}
}

// TestGate_DecisionFlipsAfterAdd is the no-restart proof: a disallowed email
// becomes allowed after an admin Add against the SAME repo the handler holds,
// with no handler reconstruction — exactly the operational win this feature
// delivers over the boot-time env map.
func TestGate_DecisionFlipsAfterAdd(t *testing.T) {
	ctx := context.Background()
	betaRepo := beta.NewSQLiteRepository(dbtest.New(t))
	// Seed one allowed email so the table is non-empty (gate active).
	if err := betaRepo.Add(ctx, "seed@example.com", "", ""); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	h := newGateHandler(t, betaRepo)

	const target = "newtester@example.com"

	before, err := h.betaChecker.IsAllowed(ctx, target)
	if err != nil {
		t.Fatalf("IsAllowed before: %v", err)
	}
	if before {
		t.Fatal("target allowed before Add, expected blocked")
	}

	// Admin adds the email at runtime — no handler restart.
	if err = betaRepo.Add(ctx, target, "admin@example.com", ""); err != nil {
		t.Fatalf("Add target: %v", err)
	}

	after, err := h.betaChecker.IsAllowed(ctx, target)
	if err != nil {
		t.Fatalf("IsAllowed after: %v", err)
	}
	if !after {
		t.Fatal("target still blocked after Add; the gate did not pick up the new email without a restart")
	}
}
