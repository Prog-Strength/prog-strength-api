package chat

import "time"

// Message is one row inside chat_messages — either a user turn or
// an assistant turn. The repo assigns Position at append time, so
// callers building Messages for AppendTurn leave it zero.
//
// Model is set on assistant rows ("claude-sonnet-4-6", etc.) so the
// history view can surface "via Sonnet" badges. ToolsJSON is an
// opaque JSON blob of any tool calls the assistant emitted during
// the turn — see the SOW for the rationale on storing as JSON vs
// normalizing into a tool_calls table.
type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	Position  int       `json:"position"`
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	Model     *string   `json:"model,omitempty"`
	ToolsJSON *string   `json:"tools_json,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Turn is the (user, assistant) pair the AppendTurn write path
// accepts. Two rows go in inside one transaction so a session can
// never be left in a half-recorded state.
//
// Turn is a transport-shape type rather than a stored shape — the
// repo materializes two Message rows from it. Defined here (vs the
// handler) so memory + sqlite repos share the input contract.
type Turn struct {
	User      Message `json:"user"`
	Assistant Message `json:"assistant"`
}

// ValidateForAppend runs on the input the handler builds from the
// client's POST body. Role and content checks per side; the
// session id + position get set by the repo.
func (t *Turn) ValidateForAppend() error {
	if t.User.Role != "" && t.User.Role != RoleUser {
		return &InvalidRoleError{Value: string(t.User.Role)}
	}
	if t.Assistant.Role != "" && t.Assistant.Role != RoleAssistant {
		return &InvalidRoleError{Value: string(t.Assistant.Role)}
	}
	if t.User.Content == "" || t.Assistant.Content == "" {
		return ErrEmptyContent
	}
	return nil
}
