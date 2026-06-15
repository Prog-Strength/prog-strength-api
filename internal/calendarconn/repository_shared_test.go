package calendarconn

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// runRepositoryContract exercises a Repository implementation against the full
// behavioral contract. Both the memory and sqlite repos run it via a factory
// so the two implementations can't drift.
func runRepositoryContract(t *testing.T, newRepo func(t *testing.T) Repository) {
	t.Helper()

	t0 := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	enc := []byte{0x01, 0x02, 0x03, 0xFF, 0x00}
	nonce := []byte{0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15}

	t.Run("UpsertThenGet_ReturnsMetadataConnected", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", enc, nonce, "primary", "scopeA scopeB", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "u1" || got.GoogleCalendarID != "primary" || got.Scopes != "scopeA scopeB" {
			t.Fatalf("metadata mismatch: %+v", got)
		}
		if got.Status != StatusConnected {
			t.Fatalf("status = %q, want connected", got.Status)
		}
		if !got.ConnectedAt.Equal(t0) {
			t.Fatalf("connected_at = %v, want %v", got.ConnectedAt, t0)
		}
		if !got.UpdatedAt.Equal(t0) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t0)
		}
	})

	t.Run("GetRefreshToken_RoundTripsExactBytes", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", enc, nonce, "primary", "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		gotEnc, gotNonce, err := repo.GetRefreshToken(ctx, "u1")
		if err != nil {
			t.Fatalf("GetRefreshToken: %v", err)
		}
		if !bytes.Equal(gotEnc, enc) {
			t.Fatalf("enc = %v, want %v", gotEnc, enc)
		}
		if !bytes.Equal(gotNonce, nonce) {
			t.Fatalf("nonce = %v, want %v", gotNonce, nonce)
		}
	})

	t.Run("SetStatusRevoked_Reflected", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", enc, nonce, "primary", "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		t1 := t0.Add(time.Hour)
		if err := repo.SetStatus(ctx, "u1", StatusRevoked, t1); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusRevoked {
			t.Fatalf("status = %q, want revoked", got.Status)
		}
		if !got.UpdatedAt.Equal(t1) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t1)
		}
		if !got.ConnectedAt.Equal(t0) {
			t.Fatalf("connected_at = %v, want preserved %v", got.ConnectedAt, t0)
		}
	})

	t.Run("ReUpsertAfterRevoke_ConnectedAgainConnectedAtPreserved", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", enc, nonce, "primary", "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := repo.SetStatus(ctx, "u1", StatusRevoked, t0.Add(time.Hour)); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}

		t2 := t0.Add(48 * time.Hour)
		newEnc := []byte{0xAA, 0xBB}
		newNonce := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2A, 0x2B, 0x2C}
		if err := repo.Upsert(ctx, "u1", newEnc, newNonce, "secondary", "s2", t2); err != nil {
			t.Fatalf("re-Upsert: %v", err)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusConnected {
			t.Fatalf("status = %q, want connected", got.Status)
		}
		if !got.ConnectedAt.Equal(t0) {
			t.Fatalf("connected_at = %v, want preserved %v", got.ConnectedAt, t0)
		}
		if !got.UpdatedAt.Equal(t2) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t2)
		}
		if got.GoogleCalendarID != "secondary" || got.Scopes != "s2" {
			t.Fatalf("re-upsert did not replace metadata: %+v", got)
		}
		gotEnc, gotNonce, err := repo.GetRefreshToken(ctx, "u1")
		if err != nil {
			t.Fatalf("GetRefreshToken: %v", err)
		}
		if !bytes.Equal(gotEnc, newEnc) || !bytes.Equal(gotNonce, newNonce) {
			t.Fatalf("re-upsert did not replace token material")
		}
	})

	t.Run("Delete_RemovesRow", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", enc, nonce, "primary", "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := repo.Delete(ctx, "u1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := repo.Get(ctx, "u1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
		}
		exists, err := repo.Exists(ctx, "u1")
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if exists {
			t.Fatal("Exists after delete = true, want false")
		}
	})

	t.Run("Exists_TrueFalse", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		exists, err := repo.Exists(ctx, "u1")
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if exists {
			t.Fatal("Exists before upsert = true, want false")
		}
		if err = repo.Upsert(ctx, "u1", enc, nonce, "primary", "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		exists, err = repo.Exists(ctx, "u1")
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if !exists {
			t.Fatal("Exists after upsert = false, want true")
		}
	})

	t.Run("AbsentUser_ReturnsErrNotFound", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if _, err := repo.Get(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get: err = %v, want ErrNotFound", err)
		}
		if _, _, err := repo.GetRefreshToken(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRefreshToken: err = %v, want ErrNotFound", err)
		}
		if err := repo.SetStatus(ctx, "ghost", StatusRevoked, t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetStatus: err = %v, want ErrNotFound", err)
		}
		if err := repo.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete: err = %v, want ErrNotFound", err)
		}
	})
}
