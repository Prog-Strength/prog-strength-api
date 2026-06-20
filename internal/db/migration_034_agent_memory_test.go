package db

import (
	"database/sql"
	"strings"
	"testing"
)

// columnExists reports whether the named table has the named column, per
// PRAGMA table_info.
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return found
}

// TestMigrate034_AgentMemory exercises migration 034 end to end on a fully
// migrated database: the agent_memories table and vec_agent_memories vec0
// virtual table exist, chat_sessions gained memory_distilled_at, a full
// insert + per-user KNN round trip works, and deleting the source session
// cascades to the memory row.
func TestMigrate034_AgentMemory(t *testing.T) {
	t.Parallel()
	conn := newEmptyDB(t)
	applyMigrationsThrough(t, conn, 0, nil)

	// agent_memories exists as a base table.
	var memTable string
	if err := conn.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'agent_memories'`,
	).Scan(&memTable); err != nil {
		t.Fatalf("agent_memories table should exist after migration 034: %v", err)
	}

	// The vec0 virtual table is queryable and starts empty.
	var vecCount int
	if err := conn.QueryRow(`SELECT count(*) FROM vec_agent_memories`).Scan(&vecCount); err != nil {
		t.Fatalf("count vec_agent_memories: %v", err)
	}
	if vecCount != 0 {
		t.Fatalf("vec_agent_memories should start empty: got %d", vecCount)
	}

	// chat_sessions gained the memory_distilled_at column.
	if !columnExists(t, conn, "chat_sessions", "memory_distilled_at") {
		t.Fatal("chat_sessions should have a memory_distilled_at column after migration 034")
	}

	// Seed a chat session to satisfy the source_session_id FK.
	const sessionID = "sess-1"
	const userID = "u1"
	const now = "2026-06-20T12:00:00Z"
	if _, err := conn.Exec(`
		INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
		VALUES (?, ?, '', ?, ?, ?)
	`, sessionID, userID, now, now, now); err != nil {
		t.Fatalf("insert chat_sessions: %v", err)
	}

	// Insert the durable memory row.
	res, err := conn.Exec(`
		INSERT INTO agent_memories (
			user_id, distilled_text, source_session_id,
			embedding_model, embedding_dim, created_at
		) VALUES (?, 'lifts heavy on mondays', ?, 'text-embedding-3-small', 1536, ?)
	`, userID, sessionID, now)
	if err != nil {
		t.Fatalf("insert agent_memories: %v", err)
	}
	memoryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Insert the matching 1536-float vector. The literal is all zeros with a
	// trailing 1, which is enough to round-trip through the KNN index.
	vec := "[" + strings.Repeat("0,", 1535) + "1]"
	if _, err := conn.Exec(
		`INSERT INTO vec_agent_memories (memory_id, user_id, embedding) VALUES (?, ?, ?)`,
		memoryID, userID, vec,
	); err != nil {
		t.Fatalf("insert vec_agent_memories: %v", err)
	}

	// Per-user KNN returns the inserted memory.
	var gotID int64
	if err := conn.QueryRow(
		`SELECT memory_id FROM vec_agent_memories WHERE user_id = ? AND embedding MATCH ? AND k = 1`,
		userID, vec,
	).Scan(&gotID); err != nil {
		t.Fatalf("knn query: %v", err)
	}
	if gotID != memoryID {
		t.Fatalf("knn returned memory_id %d, want %d", gotID, memoryID)
	}

	// Deleting the source session cascades to agent_memories (FK ON DELETE
	// CASCADE; dbtest opens with foreign_keys on).
	if _, err := conn.Exec(`DELETE FROM chat_sessions WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("delete chat_sessions: %v", err)
	}
	var remaining int
	if err := conn.QueryRow(
		`SELECT count(*) FROM agent_memories WHERE source_session_id = ?`, sessionID,
	).Scan(&remaining); err != nil {
		t.Fatalf("count agent_memories after cascade: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("agent_memories should cascade-delete with its session: got %d remaining", remaining)
	}
}
