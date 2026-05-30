package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Compile-time assertion that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is a SQLite-backed implementation. Same shape as
// the workout repo: the *sql.DB is the only dependency and `now`
// is injectable so tests can pin timestamps.
type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

// CreateSession inserts a new chat_sessions row inside a transaction
// that also evicts the user's oldest session if creating this one
// would push the user above MaxSessionsPerUser. The ON DELETE CASCADE
// on chat_messages.session_id removes evicted sessions' messages in
// the same statement.
func (r *SQLiteRepository) CreateSession(ctx context.Context, s *Session) error {
	if err := s.ValidateForCreate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Reject duplicates explicitly so the caller gets ErrSessionIDExists
	// rather than a generic UNIQUE-violation error from the PK.
	var existing int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chat_sessions WHERE id = ?`,
		s.ID,
	).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return ErrSessionIDExists
	}

	var active int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chat_sessions WHERE user_id = ? AND deleted_at IS NULL`,
		s.UserID,
	).Scan(&active); err != nil {
		return err
	}
	if active >= MaxSessionsPerUser {
		// Hard-delete the oldest active session for this user. The
		// foreign-key CASCADE removes its messages.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM chat_sessions
			WHERE id = (
				SELECT id FROM chat_sessions
				WHERE user_id = ? AND deleted_at IS NULL
				ORDER BY last_message_at ASC
				LIMIT 1
			)
		`, s.UserID); err != nil {
			return err
		}
	}

	now := r.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_sessions (
			id, user_id, title,
			created_at, updated_at, last_message_at, deleted_at
		) VALUES (?, ?, '', ?, ?, ?, NULL)
	`, s.ID, s.UserID, now, now, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.Title = ""
	s.CreatedAt = now
	s.UpdatedAt = now
	s.LastMessageAt = now
	s.DeletedAt = nil
	return nil
}

func (r *SQLiteRepository) GetSession(ctx context.Context, userID, sessionID string) (*Session, error) {
	var s Session
	err := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, title, created_at, updated_at, last_message_at, deleted_at
		FROM chat_sessions
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, sessionID, userID).Scan(
		&s.ID, &s.UserID, &s.Title,
		&s.CreatedAt, &s.UpdatedAt, &s.LastMessageAt, &s.DeletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SQLiteRepository) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, title, created_at, updated_at, last_message_at, deleted_at
		FROM chat_sessions
		WHERE user_id = ? AND deleted_at IS NULL
		ORDER BY last_message_at DESC
		LIMIT ?
	`, userID, MaxSessionsPerUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID, &s.UserID, &s.Title,
			&s.CreatedAt, &s.UpdatedAt, &s.LastMessageAt, &s.DeletedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) SetTitle(ctx context.Context, userID, sessionID, title string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_sessions
		SET title = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, title, now, sessionID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) SoftDeleteSession(ctx context.Context, userID, sessionID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_sessions
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, now, sessionID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) AppendTurn(ctx context.Context, userID, sessionID string, turn Turn) (Session, []Message, error) {
	if err := turn.ValidateForAppend(); err != nil {
		return Session{}, nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, nil, err
	}
	defer tx.Rollback()

	// Authorize + lock-up-to-the-session via SELECT. SQLite serializes
	// writes at the DB level so we don't need explicit row locks; the
	// transaction boundary is enough.
	var s Session
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, title, created_at, updated_at, last_message_at, deleted_at
		FROM chat_sessions
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, sessionID, userID).Scan(
		&s.ID, &s.UserID, &s.Title,
		&s.CreatedAt, &s.UpdatedAt, &s.LastMessageAt, &s.DeletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, nil, ErrNotFound
	}
	if err != nil {
		return Session{}, nil, err
	}

	// Find the next position by reading current max for the session.
	// COALESCE(MAX(position), -1) means an empty session starts at
	// position 0.
	var maxPos int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(position), -1) FROM chat_messages WHERE session_id = ?
	`, sessionID).Scan(&maxPos); err != nil {
		return Session{}, nil, err
	}

	now := r.now().UTC()
	userPos := maxPos + 1
	assistantPos := maxPos + 2

	userRes, err := tx.ExecContext(ctx, `
		INSERT INTO chat_messages (
			session_id, position, role, content, model, tools_json, created_at
		) VALUES (?, ?, 'user', ?, NULL, NULL, ?)
	`, sessionID, userPos, turn.User.Content, now)
	if err != nil {
		return Session{}, nil, err
	}
	userID64, err := userRes.LastInsertId()
	if err != nil {
		return Session{}, nil, err
	}

	assistantRes, err := tx.ExecContext(ctx, `
		INSERT INTO chat_messages (
			session_id, position, role, content, model, tools_json, created_at
		) VALUES (?, ?, 'assistant', ?, ?, ?, ?)
	`, sessionID, assistantPos, turn.Assistant.Content,
		turn.Assistant.Model, turn.Assistant.ToolsJSON, now)
	if err != nil {
		return Session{}, nil, err
	}
	assistantID, err := assistantRes.LastInsertId()
	if err != nil {
		return Session{}, nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_sessions
		SET last_message_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, sessionID); err != nil {
		return Session{}, nil, err
	}

	if err := tx.Commit(); err != nil {
		return Session{}, nil, err
	}

	s.LastMessageAt = now
	s.UpdatedAt = now

	userMsg := Message{
		ID:        userID64,
		SessionID: sessionID,
		Position:  userPos,
		Role:      RoleUser,
		Content:   turn.User.Content,
		CreatedAt: now,
	}
	assistantMsg := Message{
		ID:        assistantID,
		SessionID: sessionID,
		Position:  assistantPos,
		Role:      RoleAssistant,
		Content:   turn.Assistant.Content,
		Model:     turn.Assistant.Model,
		ToolsJSON: turn.Assistant.ToolsJSON,
		CreatedAt: now,
	}
	return s, []Message{userMsg, assistantMsg}, nil
}

func (r *SQLiteRepository) ListMessages(ctx context.Context, userID, sessionID string) ([]Message, error) {
	// Authorize via the session row first so the message read is
	// scoped to a verified-owned session. Same pattern as workout
	// list-by-id: cheap up-front check beats teaching the messages
	// query about user ownership.
	if _, err := r.GetSession(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, position, role, content, model, tools_json, created_at
		FROM chat_messages
		WHERE session_id = ?
		ORDER BY position ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(
			&m.ID, &m.SessionID, &m.Position, &role,
			&m.Content, &m.Model, &m.ToolsJSON, &m.CreatedAt,
		); err != nil {
			return nil, err
		}
		m.Role = Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}
