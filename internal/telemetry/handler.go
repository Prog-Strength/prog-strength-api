package telemetry

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Handler exposes HTTP endpoints under /internal/telemetry/* that
// accept agent telemetry writes. These routes are intentionally
// *not* behind the JWT middleware — they live under /internal which
// Caddy refuses to proxy, so only Docker-network traffic between the
// agent and api containers can reach them. The single-host
// deploy's network boundary is the auth boundary.
type Handler struct {
	repo Repository
	now  func() time.Time // injectable for tests
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo, now: time.Now}
}

// Mount registers the three telemetry endpoints under /internal/telemetry.
// The Caddy layer is responsible for refusing to proxy anything under
// /internal; the Go side does not duplicate that check.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/internal/telemetry", func(r chi.Router) {
		r.Post("/turns", h.turn)
		r.Post("/tool-calls", h.toolCalls)
		r.Post("/messages", h.messages)
	})
}

// --- turn ---------------------------------------------------------------

// turnRequest mirrors the AgentTurn struct in JSON-tagged form.
// Timestamps are RFC3339 strings so the wire format stays
// language-neutral (the FastAPI agent service is the primary caller).
type turnRequest struct {
	ID                  string  `json:"id"`
	UserID              string  `json:"user_id"`
	SessionID           string  `json:"session_id"`
	Model               string  `json:"model"`
	RoutedTier          string  `json:"routed_tier"`
	RouterModel         string  `json:"router_model"`
	RouterLatencyMs     int     `json:"router_latency_ms"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	TotalLatencyMs      int     `json:"total_latency_ms"`
	TimeToFirstTokenMs  int     `json:"time_to_first_token_ms"`
	CompletionReason    string  `json:"completion_reason"`
	Error               *string `json:"error"`
	StartedAt           string  `json:"started_at"`
	EndedAt             string  `json:"ended_at"`
}

func (h *Handler) turn(w http.ResponseWriter, r *http.Request) {
	var req turnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.UserID == "" || req.SessionID == "" || req.Model == "" ||
		req.RoutedTier == "" || req.CompletionReason == "" {
		httpresp.Error(w, http.StatusBadRequest, "user_id, session_id, model, routed_tier, completion_reason are required")
		return
	}

	startedAt, err := time.Parse(time.RFC3339, req.StartedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid started_at: must be RFC3339")
		return
	}
	endedAt, err := time.Parse(time.RFC3339, req.EndedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid ended_at: must be RFC3339")
		return
	}

	turnID := req.ID
	if turnID == "" {
		turnID = id.New()
	}

	t := AgentTurn{
		ID:                  turnID,
		UserID:              req.UserID,
		SessionID:           req.SessionID,
		Model:               req.Model,
		RoutedTier:          req.RoutedTier,
		RouterModel:         req.RouterModel,
		RouterLatencyMs:     req.RouterLatencyMs,
		InputTokens:         req.InputTokens,
		OutputTokens:        req.OutputTokens,
		CacheCreationTokens: req.CacheCreationTokens,
		CacheReadTokens:     req.CacheReadTokens,
		TotalLatencyMs:      req.TotalLatencyMs,
		TimeToFirstTokenMs:  req.TimeToFirstTokenMs,
		CompletionReason:    req.CompletionReason,
		Error:               req.Error,
		StartedAt:           startedAt,
		EndedAt:             endedAt,
		CreatedAt:           h.now().UTC(),
	}

	if err := h.repo.InsertTurn(r.Context(), t); err != nil {
		if errors.Is(err, ErrConflict) {
			httpresp.Error(w, http.StatusConflict, "turn already recorded")
			return
		}
		httpresp.ServerError(w, r.Context(), "insert telemetry turn", err)
		return
	}

	httpresp.Created(w, "recorded turn", map[string]string{"id": t.ID})
}

// --- tool calls ---------------------------------------------------------

type toolCallRequest struct {
	ID            string  `json:"id"`
	TurnID        string  `json:"turn_id"`
	ToolName      string  `json:"tool_name"`
	ArgumentsJSON *string `json:"arguments_json"`
	ResultSummary *string `json:"result_summary"`
	LatencyMs     int     `json:"latency_ms"`
	Error         *string `json:"error"`
	StartedAt     string  `json:"started_at"`
	EndedAt       string  `json:"ended_at"`
}

type toolCallsRequest struct {
	Calls []toolCallRequest `json:"calls"`
}

func (h *Handler) toolCalls(w http.ResponseWriter, r *http.Request) {
	var req toolCallsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	now := h.now().UTC()
	out := make([]AgentToolCall, 0, len(req.Calls))
	for i, c := range req.Calls {
		if c.TurnID == "" || c.ToolName == "" {
			httpresp.Error(w, http.StatusBadRequest, "turn_id and tool_name are required on every call")
			return
		}
		startedAt, err := time.Parse(time.RFC3339, c.StartedAt)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "calls["+itoa(i)+"]: invalid started_at")
			return
		}
		endedAt, err := time.Parse(time.RFC3339, c.EndedAt)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "calls["+itoa(i)+"]: invalid ended_at")
			return
		}
		callID := c.ID
		if callID == "" {
			callID = id.New()
		}
		out = append(out, AgentToolCall{
			ID:            callID,
			TurnID:        c.TurnID,
			ToolName:      c.ToolName,
			ArgumentsJSON: c.ArgumentsJSON,
			ResultSummary: c.ResultSummary,
			LatencyMs:     c.LatencyMs,
			Error:         c.Error,
			StartedAt:     startedAt,
			EndedAt:       endedAt,
			CreatedAt:     now,
		})
	}

	if err := h.repo.InsertToolCalls(r.Context(), out); err != nil {
		httpresp.ServerError(w, r.Context(), "insert tool calls", err)
		return
	}
	httpresp.Created(w, "recorded tool calls", map[string]int{"count": len(out)})
}

// --- messages -----------------------------------------------------------

type messageRequest struct {
	ID         string  `json:"id"`
	TurnID     string  `json:"turn_id"`
	Role       string  `json:"role"`
	Content    *string `json:"content"`
	TokenCount *int    `json:"token_count"`
	CreatedAt  string  `json:"created_at"`
}

type messagesRequest struct {
	Messages []messageRequest `json:"messages"`
}

func (h *Handler) messages(w http.ResponseWriter, r *http.Request) {
	var req messagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	out := make([]AgentMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		if m.TurnID == "" || (m.Role != "user" && m.Role != "assistant") {
			httpresp.Error(w, http.StatusBadRequest, "messages["+itoa(i)+"]: turn_id required, role must be 'user' or 'assistant'")
			return
		}
		var createdAt time.Time
		if m.CreatedAt != "" {
			t, err := time.Parse(time.RFC3339, m.CreatedAt)
			if err != nil {
				httpresp.Error(w, http.StatusBadRequest, "messages["+itoa(i)+"]: invalid created_at")
				return
			}
			createdAt = t
		} else {
			createdAt = h.now().UTC()
		}
		msgID := m.ID
		if msgID == "" {
			msgID = id.New()
		}
		out = append(out, AgentMessage{
			ID:         msgID,
			TurnID:     m.TurnID,
			Role:       m.Role,
			Content:    m.Content,
			TokenCount: m.TokenCount,
			CreatedAt:  createdAt,
		})
	}

	if err := h.repo.InsertMessages(r.Context(), out); err != nil {
		httpresp.ServerError(w, r.Context(), "insert messages", err)
		return
	}
	httpresp.Created(w, "recorded messages", map[string]int{"count": len(out)})
}

// itoa is a tiny inlined-int helper so the validation messages don't
// pull in fmt or strconv for a single-digit-of-context error string.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
