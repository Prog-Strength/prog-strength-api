package tokencrypt

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// key32 returns a deterministic 32-byte key for tests.
func key32(fill byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill
	}
	return k
}

func TestNewCipher_RejectsNon32ByteKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := NewCipher(make([]byte, n)); err == nil {
			t.Errorf("NewCipher(%d-byte key): want error, got nil", n)
		}
	}
	if _, err := NewCipher(key32(0x01)); err != nil {
		t.Errorf("NewCipher(32-byte key): unexpected error: %v", err)
	}
}

func TestCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(key32(0x07))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plaintext := []byte("1//0gRefreshTokenFromGoogle-abc123")

	ct, nonce, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(nonce) != 12 {
		t.Fatalf("nonce length = %d, want 12", len(nonce))
	}
	got, err := c.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestCipher_DecryptWrongKeyFails(t *testing.T) {
	enc, _ := NewCipher(key32(0x07))
	other, _ := NewCipher(key32(0x08))

	ct, nonce, err := enc.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := other.Decrypt(ct, nonce); err == nil {
		t.Fatal("Decrypt with wrong key: want error, got nil")
	}
}

// TestCipher_TwoKeyIsolation verifies that ciphertext sealed with a Cipher
// built from key A cannot be opened by a Cipher built from a different key B:
// the GCM authentication tag mismatch surfaces as a Decrypt error. This is the
// property that lets distinct integrations hold independent keys safely.
func TestCipher_TwoKeyIsolation(t *testing.T) {
	keyA := key32(0xAA)
	keyB := key32(0xBB)

	cipherA, err := NewCipher(keyA)
	if err != nil {
		t.Fatalf("NewCipher(A): %v", err)
	}
	cipherB, err := NewCipher(keyB)
	if err != nil {
		t.Fatalf("NewCipher(B): %v", err)
	}

	ct, nonce, err := cipherA.Encrypt([]byte("cross-integration secret"))
	if err != nil {
		t.Fatalf("Encrypt with A: %v", err)
	}
	if _, err := cipherB.Decrypt(ct, nonce); err == nil {
		t.Fatal("Decrypt with key B of ciphertext sealed by key A: want error, got nil")
	}
}

func TestCipher_TamperedCiphertextFails(t *testing.T) {
	c, _ := NewCipher(key32(0x07))
	ct, nonce, err := c.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := make([]byte, len(ct))
	copy(tampered, ct)
	tampered[0] ^= 0xFF // flip a byte
	if _, err := c.Decrypt(tampered, nonce); err == nil {
		t.Fatal("Decrypt of tampered ciphertext: want error, got nil")
	}
}

func TestCipher_FreshNoncePerCall(t *testing.T) {
	c, _ := NewCipher(key32(0x07))
	plaintext := []byte("same plaintext")

	ct1, n1, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt #1: %v", err)
	}
	ct2, n2, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt #2: %v", err)
	}
	if bytes.Equal(n1, n2) {
		t.Fatal("two Encrypt calls produced identical nonces")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two Encrypt calls produced identical ciphertext")
	}
}

func TestKeyFromEnv_Valid(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString(key32(0x42))
	got, err := KeyFromEnv(raw)
	if err != nil {
		t.Fatalf("KeyFromEnv: %v", err)
	}
	if !bytes.Equal(got, key32(0x42)) {
		t.Fatal("KeyFromEnv returned wrong bytes")
	}
	// The derived key must drive a working cipher.
	if _, err := NewCipher(got); err != nil {
		t.Fatalf("NewCipher with derived key: %v", err)
	}
}

func TestKeyFromEnv_RejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		raw := base64.StdEncoding.EncodeToString(make([]byte, n))
		if _, err := KeyFromEnv(raw); err == nil {
			t.Errorf("KeyFromEnv(%d decoded bytes): want error, got nil", n)
		}
	}
}

func TestKeyFromEnv_RejectsInvalidBase64(t *testing.T) {
	if _, err := KeyFromEnv("not valid base64 !!!"); err == nil {
		t.Fatal("KeyFromEnv(invalid base64): want error, got nil")
	}
}
