package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes HTTP endpoints for persistent chat sessions. See
// prog-strength-docs/sows/persistent-chat-sessions.md. The agent
// itself stays stateless; this handler is the API-side surface for
// listing past conversations, appending turns, and updating titles.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers chat routes. Callers wrap the chi.Router in
// auth.RequireUser before calling Mount — the handlers read userID
// from request context and assume it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/chat-sessions", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.patch)
		r.Delete("/{id}", h.delete)
		r.Post("/{id}/messages", h.appendTurn)
	})
}

// MountInternal registers chat routes that sit behind the docker
// network boundary (and Caddy's refusal to proxy /internal/*) rather
// than the user-JWT auth middleware. The only consumer is the agent
// service reading the session's prior intent on the way into /chat.
func (h *Handler) MountInternal(r chi.Router) {
	r.Route("/internal/chat-sessions", func(r chi.Router) {
		r.Get("/{id}/intent", h.getInternalIntent)
	})
}

type internalIntentResponse struct {
	Intent   *string    `json:"intent"`
	IntentAt *time.Time `json:"intent_at"`
}

func (h *Handler) getInternalIntent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	intent, at, err := h.repo.GetSessionIntent(r.Context(), id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		httpresp.ServerError(w, r.Context(), "get chat session intent", err)
		return
	}
	// SOW: 404 returns the same shape as "session has no intent yet"
	// so the agent's client doesn't have to distinguish.
	httpresp.OK(w, "got chat session intent", internalIntentResponse{
		Intent:   intent,
		IntentAt: at,
	})
}

// --- request / response DTOs --------------------------------------

// createSessionRequest is the POST /chat-sessions body. Clients mint
// the UUID so they can render optimistically; the server validates
// the format + uniqueness and rejects collisions with 409.
type createSessionRequest struct {
	ID string `json:"id"`
}

// patchSessionRequest is the PATCH /chat-sessions/{id} body. v1
// only knows about title; future fields land here.
type patchSessionRequest struct {
	Title *string `json:"title"`
}

// turnRequest is the POST /chat-sessions/{id}/messages body. Mirrors
// chat.Turn but the role enums are validated server-side via
// Turn.ValidateForAppend rather than trusted as-is on the wire.
type turnRequest struct {
	User      turnSideRequest `json:"user"`
	Assistant turnSideRequest `json:"assistant"`
}

type turnSideRequest struct {
	Content   string  `json:"content"`
	Model     *string `json:"model,omitempty"`
	ToolsJSON *string `json:"tools_json,omitempty"`
}

// sessionWithMessages is the GET /chat-sessions/{id} response shape:
// the session row plus every message in position order. Defined
// here (vs the page-level types) because the message_count denorm
// in the list response is the only other shape.
type sessionWithMessages struct {
	Session
	Messages []Message `json:"messages"`
}

// sessionListItem is one row in GET /chat-sessions. The MessageCount
// denorm lets the UI show "12 messages" badges without a follow-up
// query per row.
type sessionListItem struct {
	ID            string `json:"id"`
	UserID        string `json:"user_id"`
	Title         string `json:"title"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	LastMessageAt string `json:"last_message_at"`
	MessageCount  int    `json:"message_count"`
}

// --- handlers -----------------------------------------------------

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	sessions, err := h.repo.ListSessions(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list chat sessions", err)
		return
	}
	// MessageCount denorm: one ListMessages call per session is
	// acceptable at MaxSessionsPerUser=50 worst case, and the
	// page-level fix to make it a single COUNT-grouped query is a
	// follow-up if it ever shows up on a perf trace. The SOW
	// promises this is a single statement via JOIN — leaving as
	// follow-up is a known short-cut from that promise.
	out := make([]sessionListItem, 0, len(sessions))
	for _, s := range sessions {
		msgs, err := h.repo.ListMessages(r.Context(), userID, s.ID)
		if err != nil {
			httpresp.ServerError(w, r.Context(), "count chat messages", err)
			return
		}
		out = append(out, sessionListItem{
			ID:            s.ID,
			UserID:        s.UserID,
			Title:         s.Title,
			CreatedAt:     s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:     s.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			LastMessageAt: s.LastMessageAt.Format("2006-01-02T15:04:05Z07:00"),
			MessageCount:  len(msgs),
		})
	}
	httpresp.OK(w, "listed chat sessions", out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	var body createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s := &Session{ID: body.ID, UserID: userID}
	if err := h.repo.CreateSession(r.Context(), s); err != nil {
		switch {
		case errors.Is(err, ErrSessionIDRequired),
			errors.Is(err, ErrInvalidSessionID),
			errors.Is(err, ErrUserIDRequired):
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, ErrSessionIDExists):
			httpresp.Error(w, http.StatusConflict, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create chat session", err)
		return
	}
	httpresp.Created(w, "created chat session", s)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	session, err := h.repo.GetSession(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "chat session not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get chat session", err)
		return
	}
	msgs, err := h.repo.ListMessages(r.Context(), userID, id)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list chat messages", err)
		return
	}
	httpresp.OK(w, "got chat session", sessionWithMessages{
		Session:  *session,
		Messages: msgs,
	})
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	var body patchSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Title == nil {
		httpresp.Error(w, http.StatusBadRequest, "no patchable fields provided")
		return
	}
	normalized, err := NormalizeTitle(*body.Title)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.SetTitle(r.Context(), userID, id, normalized); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "chat session not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "set chat title", err)
		return
	}
	session, err := h.repo.GetSession(r.Context(), userID, id)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "reload chat session", err)
		return
	}
	httpresp.OK(w, "updated chat session", session)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.repo.SoftDeleteSession(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "chat session not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete chat session", err)
		return
	}
	httpresp.OK(w, "deleted chat session", nil)
}

func (h *Handler) appendTurn(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context",
			errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	var body turnRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	turn := Turn{
		User: Message{
			Role:    RoleUser,
			Content: body.User.Content,
		},
		Assistant: Message{
			Role:      RoleAssistant,
			Content:   body.Assistant.Content,
			Model:     body.Assistant.Model,
			ToolsJSON: body.Assistant.ToolsJSON,
		},
	}
	session, msgs, err := h.repo.AppendTurn(r.Context(), userID, id, turn)
	if err != nil {
		var invalidRole *InvalidRoleError
		switch {
		case errors.Is(err, ErrEmptyContent), errors.As(err, &invalidRole):
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, ErrNotFound):
			httpresp.Error(w, http.StatusNotFound, "chat session not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "append chat turn", err)
		return
	}
	httpresp.Created(w, "appended chat turn", sessionWithMessages{
		Session:  session,
		Messages: msgs,
	})
}
