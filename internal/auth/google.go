package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// googleUserInfoURL is Google's OpenID Connect userinfo endpoint. We use it
// instead of decoding the ID token ourselves because the userinfo response
// is a stable, documented JSON shape and avoids pulling in a JWKS verifier.
const googleUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

// newGoogleConfig builds the OAuth 2.0 client config. Scopes are limited to
// what we need: identifying the user by their verified email address.
func newGoogleConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}
}

// googleUser is the subset of Google's userinfo response we care about.
type googleUser struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// fetchGoogleUser exchanges an OAuth authorization code for an access token
// and uses it to look up the authenticated user's email and display name.
func fetchGoogleUser(ctx context.Context, cfg *oauth2.Config, code string) (*googleUser, error) {
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build userinfo request: %w", err)
	}
	resp, err := cfg.Client(ctx, token).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch userinfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned %d", resp.StatusCode)
	}
	var u googleUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	if u.Email == "" {
		return nil, fmt.Errorf("userinfo missing email")
	}
	return &u, nil
}
