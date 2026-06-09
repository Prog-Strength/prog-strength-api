package activity

import "errors"

var (
	// ErrNotFound is returned when an activity does not exist, was
	// soft-deleted, or belongs to a different user (deliberately
	// indistinguishable so IDs can't be enumerated cross-user).
	ErrNotFound = errors.New("activity: not found")

	// ErrDuplicate is returned when an activity with the same
	// (user_id, ingest_source, source_activity_id) already exists.
	// The handler maps this to a 409 and surfaces the existing
	// activity.
	ErrDuplicate = errors.New("activity: duplicate source activity")

	// ErrStorage is returned when archiving the TCX file to object
	// storage fails. The DB transaction is rolled back so an activity
	// is never persisted without its backing file.
	ErrStorage = errors.New("activity: tcx storage failed")
)
