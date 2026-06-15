package user

import (
	"time"
	"unicode/utf8"
)

// Profile validation bounds. DisplayNameMaxLen caps the editable name so the
// sidebar account row can't be broken by an arbitrarily long value (the cap
// is in runes, not bytes). HeightCmMin/Max bound the optional height metric.
const (
	DisplayNameMaxLen = 60
	HeightCmMin       = 50.0
	HeightCmMax       = 250.0
)

// User is an authenticated account. Authentication is OAuth-only; there are
// no password fields. Email is the OAuth identifier and is immutable through
// the Update path (changing email requires re-verification, not yet implemented).
type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// Username is the user-settable public handle, stored canonicalized
	// (lowercased) so uniqueness is case-insensitive. nil until first set.
	// Validation happens at the write edge (ValidateUsername), not in
	// Validate(): the stored value is already canonical, and a nil handle is
	// legal for existing/unset users.
	Username     *string      `json:"username"`
	WeightUnit   WeightUnit   `json:"weight_unit"`
	DistanceUnit DistanceUnit `json:"distance_unit"`
	// HeightCm is an optional static body metric in canonical centimeters.
	HeightCm *float64 `json:"height_cm"`
	// Timezone is the canonical IANA timezone used for server-side Google
	// Calendar writes (defaults to UTC).
	Timezone string `json:"timezone"`
	// CalendarDefaultDetail is the default calendar event detail level when
	// planning workouts: "time_block" or "full_agenda".
	CalendarDefaultDetail string `json:"calendar_default_detail"`
	// AvatarKey is the S3 object key of the user's uploaded avatar, or nil
	// when none is set. It is never serialized raw — the resolved avatar_url
	// (a presigned GET, or the OAuth fallback) is produced at the GET /me edge.
	AvatarKey *string `json:"-"`
	// OAuthAvatarURL is the avatar URL from the OAuth provider (Google's
	// `picture` claim). It's the fallback when AvatarKey is nil; also never
	// serialized raw.
	OAuthAvatarURL *string    `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"-"`
}

// Validate checks that the user has all required fields and that all enum
// values are recognized. Returns the first error encountered.
func (u *User) Validate() error {
	if u.Email == "" {
		return ErrEmailRequired
	}
	if u.DisplayName == "" {
		return ErrDisplayNameRequired
	}
	if utf8.RuneCountInString(u.DisplayName) > DisplayNameMaxLen {
		return ErrDisplayNameTooLong
	}
	if !u.WeightUnit.Valid() {
		return &InvalidEnumError{Field: "weight_unit", Value: string(u.WeightUnit)}
	}
	if !u.DistanceUnit.Valid() {
		return &InvalidEnumError{Field: "distance_unit", Value: string(u.DistanceUnit)}
	}
	if u.HeightCm != nil && (*u.HeightCm < HeightCmMin || *u.HeightCm > HeightCmMax) {
		return ErrHeightOutOfRange
	}
	if u.Timezone == "" {
		return ErrInvalidTimezone
	}
	if _, err := time.LoadLocation(u.Timezone); err != nil {
		return ErrInvalidTimezone
	}
	if u.CalendarDefaultDetail != "time_block" && u.CalendarDefaultDetail != "full_agenda" {
		return ErrInvalidCalendarDetail
	}
	return nil
}
