package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user/handle"
)

// migration029 backfills handles for existing users created before usernames
// were auto-assigned at signup. It replays the frozen GenerateHandle logic
// (name slug, then id-derived fallback, each with numeric-suffix collision
// retry) over every live user whose username is still NULL.
//
// Idempotent: a re-run finds no NULL-username rows and is a no-op. Collisions
// never abort the batch — because the whole migration runs in ONE transaction
// (see applyMigration), per-user commits aren't possible, so we resolve
// collisions with the same in-transaction suffix retry GenerateHandle already
// uses: handles assigned earlier in this run are visible to existsInTx via the
// tx, so each user gets a distinct handle. DB-only and frozen: it reads/writes
// raw SQL through its tx and calls only the pure handle.GenerateHandle helper
// (the leaf package internal/user re-exports), so a rebuilt DB always
// reconciles identically regardless of future signup changes.
func migration029() goMigration {
	return goMigration{
		Version: 29,
		Name:    "backfill_usernames",
		Run:     backfillUsernames,
	}
}

// bfNullUsernameUser is one live user still missing a handle.
type bfNullUsernameUser struct {
	id          string
	displayName string
}

func backfillUsernames(ctx context.Context, tx *sql.Tx) error {
	users, err := bfLoadNullUsernameUsers(ctx, tx)
	if err != nil {
		return err
	}

	// existsInTx reports whether a candidate handle is already taken by a live
	// user in THIS transaction, so handles assigned earlier in this run count
	// toward collisions and every user ends up with a distinct value.
	existsInTx := func(candidate string) (bool, error) {
		var one int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM users WHERE username = ? AND deleted_at IS NULL`,
			candidate,
		).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}

	// updated_at stamps when this backfill assigned the handle, mirroring how
	// migration 028 stamps the current time on the rows it touches.
	now := time.Now().UTC()
	for _, u := range users {
		username, err := handle.GenerateHandle(u.displayName, u.id, existsInTx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET username = ?, updated_at = ? WHERE id = ?`,
			username, now, u.id,
		); err != nil {
			return err
		}
	}
	return nil
}

// bfLoadNullUsernameUsers collects every live, handle-less user up front so the
// UPDATEs below don't mutate the rows the cursor is still iterating. Close is
// deferred (sqlclosecheck) and rows.Err() is returned (rowserrcheck).
func bfLoadNullUsernameUsers(ctx context.Context, tx *sql.Tx) ([]bfNullUsernameUser, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, display_name FROM users WHERE username IS NULL AND deleted_at IS NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []bfNullUsernameUser
	for rows.Next() {
		var u bfNullUsernameUser
		if err := rows.Scan(&u.id, &u.displayName); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
