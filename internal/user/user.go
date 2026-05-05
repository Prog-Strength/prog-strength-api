package user

import "time"

// User is an authenticated account. Authentication is OAuth-only; there are
// no password fields. Email is the OAuth identifier and is immutable through
// the Update path (changing email requires re-verification, not yet implemented).
type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	WeightUnit  WeightUnit `json:"weight_unit"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"-"`
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
	if !u.WeightUnit.Valid() {
		return &InvalidEnumError{Field: "weight_unit", Value: string(u.WeightUnit)}
	}
	return nil
}
