package whoopconn

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// runRepositoryContract exercises a Repository implementation against the full
// behavioral contract. Implementations run it via a factory so they can't drift.
func runRepositoryContract(t *testing.T, newRepo func(t *testing.T) Repository) {
	t.Helper()

	t0 := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	exp := t0.Add(time.Hour)
	const whoopID int64 = 987654

	tokens := TokenBundle{
		AccessTokenEnc:    []byte{0x01, 0x02, 0x03, 0xFF, 0x00},
		AccessTokenNonce:  []byte{0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15},
		RefreshTokenEnc:   []byte{0x30, 0x31, 0x32},
		RefreshTokenNonce: []byte{0x40, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4A, 0x4B},
		ExpiresAt:         exp,
	}

	t.Run("UpsertThenGet_ReturnsMetadataConnected", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "read:recovery read:sleep", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "u1" || got.WhoopUserID != whoopID || got.Scopes != "read:recovery read:sleep" {
			t.Fatalf("metadata mismatch: %+v", got)
		}
		if got.Status != StatusConnected {
			t.Fatalf("status = %q, want connected", got.Status)
		}
		if !got.TokenExpiresAt.Equal(exp) {
			t.Fatalf("token_expires_at = %v, want %v", got.TokenExpiresAt, exp)
		}
		if !got.ConnectedAt.Equal(t0) {
			t.Fatalf("connected_at = %v, want %v", got.ConnectedAt, t0)
		}
		if !got.UpdatedAt.Equal(t0) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t0)
		}
	})

	t.Run("GetTokens_RoundTripsExactBytes", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.GetTokens(ctx, "u1")
		if err != nil {
			t.Fatalf("GetTokens: %v", err)
		}
		if !bytes.Equal(got.AccessTokenEnc, tokens.AccessTokenEnc) ||
			!bytes.Equal(got.AccessTokenNonce, tokens.AccessTokenNonce) ||
			!bytes.Equal(got.RefreshTokenEnc, tokens.RefreshTokenEnc) ||
			!bytes.Equal(got.RefreshTokenNonce, tokens.RefreshTokenNonce) {
			t.Fatalf("token bytes mismatch: %+v", got)
		}
		if !got.ExpiresAt.Equal(exp) {
			t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, exp)
		}
	})

	t.Run("GetByWhoopUserID_FindsAndErrNotFound", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.GetByWhoopUserID(ctx, whoopID)
		if err != nil {
			t.Fatalf("GetByWhoopUserID: %v", err)
		}
		if got.UserID != "u1" || got.WhoopUserID != whoopID {
			t.Fatalf("GetByWhoopUserID mismatch: %+v", got)
		}
		if _, err := repo.GetByWhoopUserID(ctx, 111111); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByWhoopUserID unknown: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("UpdateTokens_RotatesTokensAndExpiry", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		t1 := t0.Add(30 * time.Minute)
		newExp := t1.Add(time.Hour)
		rotated := TokenBundle{
			AccessTokenEnc:    []byte{0xAA, 0xBB},
			AccessTokenNonce:  []byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5A, 0x5B, 0x5C},
			RefreshTokenEnc:   []byte{0xCC, 0xDD, 0xEE},
			RefreshTokenNonce: []byte{0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6A, 0x6B, 0x6C},
			ExpiresAt:         newExp,
		}
		if err := repo.UpdateTokens(ctx, "u1", rotated, t1); err != nil {
			t.Fatalf("UpdateTokens: %v", err)
		}
		gotTok, err := repo.GetTokens(ctx, "u1")
		if err != nil {
			t.Fatalf("GetTokens: %v", err)
		}
		if !bytes.Equal(gotTok.AccessTokenEnc, rotated.AccessTokenEnc) ||
			!bytes.Equal(gotTok.RefreshTokenEnc, rotated.RefreshTokenEnc) ||
			!gotTok.ExpiresAt.Equal(newExp) {
			t.Fatalf("UpdateTokens did not rotate: %+v", gotTok)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusConnected || !got.TokenExpiresAt.Equal(newExp) || !got.UpdatedAt.Equal(t1) {
			t.Fatalf("Get after UpdateTokens: %+v", got)
		}
		if !got.ConnectedAt.Equal(t0) {
			t.Fatalf("connected_at = %v, want preserved %v", got.ConnectedAt, t0)
		}
	})

	t.Run("SetStatusError_Reflected", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		t1 := t0.Add(time.Hour)
		if err := repo.SetStatus(ctx, "u1", StatusError, t1); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		got, err := repo.Get(ctx, "u1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusError {
			t.Fatalf("status = %q, want error", got.Status)
		}
		if !got.UpdatedAt.Equal(t1) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t1)
		}
	})

	t.Run("Revoke_WipesTokensAndSetsStatus", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		t1 := t0.Add(time.Hour)
		if err := repo.Revoke(ctx, "u1", t1); err != nil {
			t.Fatalf("Revoke: %v", err)
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
		gotTok, err := repo.GetTokens(ctx, "u1")
		if err != nil {
			t.Fatalf("GetTokens after revoke: %v", err)
		}
		if len(gotTok.AccessTokenEnc) != 0 || len(gotTok.AccessTokenNonce) != 0 ||
			len(gotTok.RefreshTokenEnc) != 0 || len(gotTok.RefreshTokenNonce) != 0 {
			t.Fatalf("Revoke did not wipe token blobs: %+v", gotTok)
		}
	})

	t.Run("ReUpsertAfterRevoke_ConnectedAgainConnectedAtPreserved", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := repo.Revoke(ctx, "u1", t0.Add(time.Hour)); err != nil {
			t.Fatalf("Revoke: %v", err)
		}

		t2 := t0.Add(48 * time.Hour)
		newExp := t2.Add(time.Hour)
		reconnect := TokenBundle{
			AccessTokenEnc:    []byte{0x71, 0x72},
			AccessTokenNonce:  []byte{0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8A, 0x8B, 0x8C},
			RefreshTokenEnc:   []byte{0x91, 0x92, 0x93},
			RefreshTokenNonce: []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8, 0xA9, 0xAA, 0xAB, 0xAC},
			ExpiresAt:         newExp,
		}
		if err := repo.Upsert(ctx, "u1", whoopID, reconnect, "s2", t2); err != nil {
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
		if got.Scopes != "s2" || !got.TokenExpiresAt.Equal(newExp) {
			t.Fatalf("re-upsert did not replace metadata: %+v", got)
		}
		gotTok, err := repo.GetTokens(ctx, "u1")
		if err != nil {
			t.Fatalf("GetTokens: %v", err)
		}
		if !bytes.Equal(gotTok.AccessTokenEnc, reconnect.AccessTokenEnc) ||
			!bytes.Equal(gotTok.RefreshTokenEnc, reconnect.RefreshTokenEnc) {
			t.Fatalf("re-upsert did not replace token material")
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
		if err = repo.Upsert(ctx, "u1", whoopID, tokens, "s", t0); err != nil {
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
		if _, err := repo.GetByWhoopUserID(ctx, 424242); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByWhoopUserID: err = %v, want ErrNotFound", err)
		}
		if _, err := repo.GetTokens(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetTokens: err = %v, want ErrNotFound", err)
		}
		if err := repo.UpdateTokens(ctx, "ghost", tokens, t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateTokens: err = %v, want ErrNotFound", err)
		}
		if err := repo.SetStatus(ctx, "ghost", StatusError, t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetStatus: err = %v, want ErrNotFound", err)
		}
		if err := repo.Revoke(ctx, "ghost", t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Revoke: err = %v, want ErrNotFound", err)
		}
	})
}
