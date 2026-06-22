package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestMigrate035_MultiSourceMemory verifies the agent_memories rebuild
// preserves existing chat rows (stamped source_type='chat_session') and id
// values, that the CHECK constraint enforces FK/discriminator agreement, that
// the new source_workout_id cascade works, and that workouts gained
// memory_distilled_at.
func TestMigrate035_MultiSourceMemory(t *testing.T) {
	t.Parallel()
	conn := newEmptyDB(t)

	// Pause right before 035 to seed a chat-sourced memory under the OLD schema.
	applyMigrationsThrough(t, conn, 35, func(t *testing.T, db *sql.DB) {
		const now = "2026-06-20T12:00:00Z"
		if _, err := db.Exec(`INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at) VALUES ('sess-1','u1','',?,?,?)`, now, now, now); err != nil {
			t.Fatalf("seed chat_sessions: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_session_id, embedding_model, embedding_dim, created_at) VALUES ('u1','squats monday','sess-1','text-embedding-3-small',1536,?)`, now); err != nil {
			t.Fatalf("seed agent_memories: %v", err)
		}
		// Seed the matching vec row so we can prove the join key survives.
		vec := "[" + strings.Repeat("0,", 1535) + "1]"
		var id int64
		if err := db.QueryRow(`SELECT id FROM agent_memories WHERE source_session_id='sess-1'`).Scan(&id); err != nil {
			t.Fatalf("read seeded id: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO vec_agent_memories (memory_id, user_id, embedding) VALUES (?, 'u1', ?)`, id, vec); err != nil {
			t.Fatalf("seed vec row: %v", err)
		}
	})

	// Existing row carried over, stamped chat_session, id preserved, vec join intact.
	var srcType string
	var id int64
	if err := conn.QueryRow(`SELECT id, source_type FROM agent_memories WHERE source_session_id='sess-1'`).Scan(&id, &srcType); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if srcType != "chat_session" {
		t.Fatalf("existing rows must be stamped chat_session, got %q", srcType)
	}
	var vecMemID int64
	if err := conn.QueryRow(`SELECT memory_id FROM vec_agent_memories WHERE memory_id=?`, id).Scan(&vecMemID); err != nil {
		t.Fatalf("vec join key not preserved for id %d: %v", id, err)
	}

	// workouts gained the distillation marker.
	if !columnExists(t, conn, "workouts", "memory_distilled_at") {
		t.Fatal("workouts should have memory_distilled_at after 035")
	}

	// CHECK rejects a chat_session row missing its session FK.
	if _, err := conn.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, embedding_model, embedding_dim, created_at) VALUES ('u1','x','chat_session','m',1,'2026-06-20T12:00:00Z')`); err == nil {
		t.Fatal("CHECK should reject chat_session with NULL source_session_id")
	}
	// CHECK rejects a workout_note row whose session FK is also set. sess-1 is a
	// valid chat_sessions FK target, so a CHECK error (not an FK error) proves the
	// discriminator/FK agreement is enforced.
	if _, err := conn.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_session_id, source_workout_id, embedding_model, embedding_dim, created_at) VALUES ('u1','x','workout_note','sess-1','w1','m',1,'2026-06-20T12:00:00Z')`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("CHECK should reject workout_note with a non-NULL source_session_id, got: %v", err)
	}

	// A workout-note memory cascades when its workout is deleted.
	const now = "2026-06-20T12:00:00Z"
	if _, err := conn.Exec(`INSERT INTO workouts (id, user_id, name, performed_at, notes, created_at, updated_at) VALUES ('w1','u1','leg day','2026-06-20T10:00:00Z','felt strong',?,?)`, now, now); err != nil {
		t.Fatalf("seed workout: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO agent_memories (user_id, distilled_text, source_type, source_workout_id, embedding_model, embedding_dim, created_at) VALUES ('u1','strong on leg day','workout_note','w1','text-embedding-3-small',1536,?)`, now); err != nil {
		t.Fatalf("insert workout-note memory: %v", err)
	}
	if _, err := conn.Exec(`DELETE FROM workouts WHERE id='w1'`); err != nil {
		t.Fatalf("delete workout: %v", err)
	}
	var remaining int
	if err := conn.QueryRow(`SELECT count(*) FROM agent_memories WHERE source_workout_id='w1'`).Scan(&remaining); err != nil {
		t.Fatalf("count after cascade: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("workout-note memory should cascade-delete with its workout, %d remaining", remaining)
	}

	// The new index exists.
	var idx string
	if err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_agent_memories_source_workout'`).Scan(&idx); err != nil {
		t.Fatalf("idx_agent_memories_source_workout should exist: %v", err)
	}
}
