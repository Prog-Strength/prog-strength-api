package telemetry

import "errors"

// ErrConflict is returned by InsertTurn when the row's ID is already
// present in the database. The handler translates this to a 409 so
// idempotent retries from the agent are explicit, not silently
// hidden behind a second 201.
var ErrConflict = errors.New("telemetry: row already exists")
