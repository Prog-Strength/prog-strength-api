package running

import "errors"

var (
	// ErrNotFound is returned when a session does not exist, was
	// soft-deleted, or belongs to a different user (deliberately
	// indistinguishable so IDs can't be enumerated cross-user).
	ErrNotFound = errors.New("running: not found")

	// ErrDuplicate is returned when a session with the same
	// (user_id, garmin_activity_id) already exists. The handler maps
	// this to a 409 and surfaces the existing session.
	ErrDuplicate = errors.New("running: duplicate garmin activity")

	// ErrStorage is returned when archiving the TCX file to object
	// storage fails. The DB transaction is rolled back so a session is
	// never persisted without its backing file.
	ErrStorage = errors.New("running: tcx storage failed")
)
