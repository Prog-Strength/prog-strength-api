package nutrition

import "errors"

var (
	// ErrNotFound is returned when a row does not exist, was
	// soft-deleted, or belongs to a different user (deliberately
	// indistinguishable so IDs can't be enumerated cross-user).
	ErrNotFound = errors.New("nutrition: not found")

	// ErrNameRequired is returned when a pantry item is created or
	// updated without a non-blank name.
	ErrNameRequired = errors.New("nutrition: name is required")

	// ErrServingUnitRequired is returned when a pantry item is created
	// or updated without a non-blank serving_unit.
	ErrServingUnitRequired = errors.New("nutrition: serving_unit is required")

	// ErrMacrosNegative is returned when any macro field is negative.
	// Per-serving values must be ≥ 0 — zero is legal (water).
	ErrMacrosNegative = errors.New("nutrition: macros must be non-negative")

	// ErrServingSizeNonPositive is returned when serving_size ≤ 0.
	ErrServingSizeNonPositive = errors.New("nutrition: serving_size must be > 0")

	// ErrQuantityNonPositive is returned when a log entry's quantity ≤ 0.
	ErrQuantityNonPositive = errors.New("nutrition: quantity must be > 0")

	// ErrLogEntryReferenceRequired is returned when a log entry is
	// created with neither pantry_item_id nor recipe_id set, or with
	// both set. The schema CHECK enforces this too; this error gets
	// caller-friendly handler-side validation in front.
	ErrLogEntryReferenceRequired = errors.New(
		"nutrition: log entry must reference exactly one pantry item or recipe",
	)
)
