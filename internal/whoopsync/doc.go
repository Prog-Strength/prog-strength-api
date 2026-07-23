// Package whoopsync integrates the app with WHOOP: the OAuth 2.0 consent flow
// (authorize / token exchange / refresh / revoke) and a thin client over the
// WHOOP v2 REST API (profile, recovery, and cycle data).
//
// The OAuth state parameter is bound to both the CSRF cookie and the
// originating user via an HMAC-SHA256 signature (see encodeState/decodeState),
// mirroring internal/calendarsync so the callback cannot be tricked into
// linking a WHOOP account to a victim's user id.
//
// WHOOP rotates the refresh token on every refresh, so Tokens always carries
// the newest refresh token and callers must persist it after each Refresh.
package whoopsync
