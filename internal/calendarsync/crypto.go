// Package calendarsync holds the cryptographic substrate for opt-in Google
// Calendar sync: an AES-256-GCM cipher used to encrypt Google refresh tokens
// at rest. OAuth handlers and Google API calls live in sibling packages /
// later tasks — this package is storage-only.
package calendarsync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// keySize is the AES-256 key length in bytes.
const keySize = 32

// Cipher is an AES-256-GCM authenticated cipher for token material. Build one
// with NewCipher; it is safe for concurrent use.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds an AES-256-GCM cipher. key must be exactly 32 bytes;
// otherwise an error is returned.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("calendarsync: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext and returns the ciphertext along with a fresh random
// 12-byte nonce. The nonce must be stored alongside the ciphertext and passed
// back to Decrypt; it is not secret.
func (c *Cipher) Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("calendarsync: read nonce: %w", err)
	}
	ciphertext = c.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Decrypt reverses Encrypt. It returns an error when the key is wrong, the
// nonce is malformed, or the ciphertext has been tampered with (GCM auth tag
// mismatch).
func (c *Cipher) Decrypt(ciphertext, nonce []byte) (plaintext []byte, err error) {
	if len(nonce) != c.aead.NonceSize() {
		return nil, fmt.Errorf("calendarsync: nonce must be %d bytes, got %d", c.aead.NonceSize(), len(nonce))
	}
	plaintext, err = c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: decrypt: %w", err)
	}
	return plaintext, nil
}

// KeyFromEnv decodes the operator-supplied CALENDAR_TOKEN_ENC_KEY value into a
// 32-byte AES-256 key. raw must be standard base64 encoding of exactly 32
// random bytes — generate one with `openssl rand -base64 32`. A wrong decoded
// length or invalid base64 yields a clear error so misconfiguration fails fast
// at startup rather than at first encrypt.
func KeyFromEnv(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("calendarsync: CALENDAR_TOKEN_ENC_KEY is empty")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("calendarsync: CALENDAR_TOKEN_ENC_KEY is not valid base64: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("calendarsync: CALENDAR_TOKEN_ENC_KEY must decode to %d bytes, got %d", keySize, len(key))
	}
	return key, nil
}
