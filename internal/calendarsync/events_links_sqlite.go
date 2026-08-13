package calendarsync

import (
	"context"
	"database/sql"
	"time"
)

var _ EventLinkRepository = (*SQLiteEventLinkRepository)(nil)

// SQLiteEventLinkRepository reads the two tables that already record what
// Prog Strength wrote to Google: planned_workouts.google_event_id (migration
// 025) and activity_calendar_sync.google_event_id (migration 053). It adds no
// schema of its own.
type SQLiteEventLinkRepository struct {
	db *sql.DB
}

func NewSQLiteEventLinkRepository(db *sql.DB) *SQLiteEventLinkRepository {
	return &SQLiteEventLinkRepository{db: db}
}

// LinksForUser runs one query per table.
//
// The plan side is bounded by scheduled start, which rides the existing
// (user_id, scheduled_start_utc) index. The activity side is bounded by user
// alone: activity_calendar_sync carries no timestamp of the session it
// describes, and joining `activities` to get one would buy a narrower map
// that is probed only by id anyway. Its (user_id, sync_status) index makes
// the user-scoped read cheap, and a user has one row per synced activity.
func (r *SQLiteEventLinkRepository) LinksForUser(ctx context.Context, userID string, from, to time.Time) (map[string]EventLink, error) {
	links := make(map[string]EventLink)

	planRows, err := r.db.QueryContext(ctx, `
		SELECT id, google_event_id
		FROM planned_workouts
		WHERE user_id = ?
		  AND google_event_id IS NOT NULL AND google_event_id != ''
		  AND deleted_at IS NULL
		  AND scheduled_start_utc >= ? AND scheduled_start_utc < ?
	`, userID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer planRows.Close()
	for planRows.Next() {
		var id, eventID string
		if scanErr := planRows.Scan(&id, &eventID); scanErr != nil {
			return nil, scanErr
		}
		links[eventID] = EventLink{Kind: LinkKindPlannedWorkout, ID: id}
	}
	if rowsErr := planRows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	actRows, err := r.db.QueryContext(ctx, `
		SELECT activity_id, google_event_id
		FROM activity_calendar_sync
		WHERE user_id = ?
		  AND google_event_id IS NOT NULL AND google_event_id != ''
	`, userID)
	if err != nil {
		return nil, err
	}
	defer actRows.Close()
	for actRows.Next() {
		var id, eventID string
		if scanErr := actRows.Scan(&id, &eventID); scanErr != nil {
			return nil, scanErr
		}
		// A plan that handed its event over to the activity that completed it
		// leaves BOTH rows pointing at the same id. The activity is what
		// actually happened, so it wins the link.
		links[eventID] = EventLink{Kind: LinkKindActivity, ID: id}
	}
	return links, actRows.Err()
}
