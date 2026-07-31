package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestMigrate045_ActivityNoteMemory verifies the agent_memories rebuild that
// admits the third source type: existing chat and workout rows carry over with
// their ids (so the vec_agent_memories join survives), the widened CHECK
// accepts an 'activity_note' row on the shared source_workout_id FK while
// still enforcing discriminator/FK agreement, and an endurance-note memory
// cascades with its activity.
func TestMigrate045_ActivityNoteMemory(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)

	const now = "2026-07-20T12:00:00Z"
	var chatMemID, workoutMemID int64

	// Pause right before 045 to seed one memory of each source type that
	// exists in the pre-045 world, plus their vec rows.
	applyMigrationsThrough(t, db, 45, 45, func(t *testing.T, conn *sql.DB) {
		seedUser(t, conn, "u1")
		mustSeed := func(query string, args ...any) {
			t.Helper()
			if _, err := conn.Exec(query, args...); err != nil {
				t.Fatalf("seed %q: %v", query, err)
			}
		}
		mustSeed(`INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
			VALUES ('sess-1', 'u1', '', ?, ?, ?)`, now, now, now)
		mustSeed(`INSERT INTO activities (id, user_id, activity_type, start_time, ingest_source, notes, created_at, updated_at)
			VALUES ('w1', 'u1', 'strength_training', '2026-07-20T10:00:00Z', 'manual', 'felt strong', ?, ?)`, now, now)
		mustSeed(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_session_id, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'trains mornings', 'chat_session', 'sess-1', 'text-embedding-3-small', 1536, ?)`, now)
		mustSeed(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_workout_id, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'left shoulder cranky on presses', 'workout_note', 'w1', 'text-embedding-3-small', 1536, ?)`, now)

		chatMemID = int64(queryInt(t, conn, `SELECT id FROM agent_memories WHERE source_session_id = 'sess-1'`))
		workoutMemID = int64(queryInt(t, conn, `SELECT id FROM agent_memories WHERE source_workout_id = 'w1'`))
		vec := "[" + strings.Repeat("0,", 1535) + "1]"
		for _, id := range []int64{chatMemID, workoutMemID} {
			mustSeed(`INSERT INTO vec_agent_memories (memory_id, user_id, embedding) VALUES (?, 'u1', ?)`, id, vec)
		}
	})

	t.Run("existing rows and their vectors survive", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM agent_memories`); got != 2 {
			t.Fatalf("agent_memories rows = %d, want 2", got)
		}
		if got := queryString(t, db, `SELECT source_type FROM agent_memories WHERE id = ?`, chatMemID); got != "chat_session" {
			t.Errorf("chat memory source_type = %q, want chat_session", got)
		}
		if got := queryString(t, db, `SELECT distilled_text FROM agent_memories WHERE id = ?`, workoutMemID); got != "left shoulder cranky on presses" {
			t.Errorf("workout memory text = %q", got)
		}
		// The vec join key is the agent_memories id: if the rebuild had
		// renumbered rows, these lookups would miss.
		for _, id := range []int64{chatMemID, workoutMemID} {
			if got := queryInt(t, db, `SELECT COUNT(*) FROM vec_agent_memories WHERE memory_id = ?`, id); got != 1 {
				t.Errorf("vec row for memory %d = %d, want 1", id, got)
			}
		}
	})

	t.Run("activity_note is accepted on the shared FK", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO activities (id, user_id, activity_type, start_time, ingest_source, notes, created_at, updated_at)
			VALUES ('a1', 'u1', 'running', '2026-07-21T07:00:00Z', 'manual_tcx', 'knee twinge at mile 4', ?, ?)`, now, now); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_workout_id, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'right knee flares past 4 miles', 'activity_note', 'a1', 'text-embedding-3-small', 1536, ?)`, now); err != nil {
			t.Fatalf("insert activity-note memory: %v", err)
		}
	})

	t.Run("CHECK still enforces discriminator/FK agreement", func(t *testing.T) {
		// activity_note with no activity FK at all.
		if _, err := db.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'x', 'activity_note', 'm', 1, ?)`, now); err == nil {
			t.Error("CHECK should reject activity_note with a NULL source_workout_id")
		}
		// activity_note carrying a chat FK. sess-1 is a valid FK target, so a
		// CHECK error (not an FK error) proves the discriminator is enforced.
		if _, err := db.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_session_id, source_workout_id, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'x', 'activity_note', 'sess-1', 'a1', 'm', 1, ?)`, now); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("CHECK should reject activity_note with a non-NULL source_session_id, got: %v", err)
		}
		// An unregistered source type is still rejected outright.
		if _, err := db.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_workout_id, embedding_model, embedding_dim, created_at)
			VALUES ('u1', 'x', 'sleep_note', 'a1', 'm', 1, ?)`, now); err == nil {
			t.Error("CHECK should reject an unknown source_type")
		}
	})

	t.Run("an activity-note memory cascades with its activity", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM activities WHERE id = 'a1'`); err != nil {
			t.Fatalf("hard delete run: %v", err)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM agent_memories WHERE source_workout_id = 'a1'`); got != 0 {
			t.Errorf("activity-note memories after cascade = %d, want 0", got)
		}
	})

	t.Run("foreign keys clean", func(t *testing.T) {
		rows, err := db.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatalf("foreign_key_check: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Error("foreign_key_check returned rows, want none")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("foreign_key_check rows: %v", err)
		}
	})

	t.Run("schema identical to a fresh database", func(t *testing.T) {
		fresh := newMigratedDB(t)
		migrated := queryString(t, db, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'agent_memories'`)
		scratch := queryString(t, fresh, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'agent_memories'`)
		if migrated != scratch {
			t.Errorf("agent_memories DDL diverges between migrated and fresh DBs:\nmigrated: %s\nfresh:    %s", migrated, scratch)
		}
	})
}
