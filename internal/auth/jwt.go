package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTLifetime is how long an issued token remains valid.
//
// This is the primary security knob for token expiry. Shorter is safer
// (smaller window if a token leaks) at the cost of more frequent re-auth.
// Refresh tokens are intentionally not implemented yet; when a token
// expires the user must complete the OAuth flow again. Tune by editing
// this constant — there is no env-var override on purpose, since changing
// token lifetime should be a deliberate code change.
const JWTLifetime = 7 * 24 * time.Hour

// Sign issues a JWT for the given user ID, valid for JWTLifetime.
// The user ID is stored in the standard "sub" (subject) claim.
func Sign(userID string, secret []byte) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(JWTLifetime)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// Parse validates a signed JWT and returns the user ID from its subject claim.
// Expired tokens, tokens signed with a different key, and tokens using an
// unexpected algorithm all return an error.
func Parse(tokenStr string, secret []byte) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		// Pin to HMAC. Without this check, a token signed with the "none"
		// algorithm or an asymmetric key would be accepted.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return "", err
	}
	if claims.Subject == "" {
		return "", errors.New("token missing subject")
	}
	return claims.Subject, nil
}
