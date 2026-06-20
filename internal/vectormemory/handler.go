package vectormemory

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes the HTTP surface for agent vector memory: the agent's
// internal retrieval endpoint and the admin inspection/probe endpoints.
//
// The two surfaces share ONE retrieval code path (svc.Retrieve) — the admin
// search endpoint deliberately calls the same method the agent uses, so the
// probe is a faithful mirror of what the agent would recall rather than a
// separate query that could drift from production behavior.
type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// MountInternal registers the agent-facing retrieval endpoint under
// /internal/memory. Like the telemetry/chat internal routes, this sits behind
// the docker-network boundary (Caddy refuses to proxy /internal/*) rather than
// the JWT middleware — the network boundary is the auth boundary.
func (h *Handler) MountInternal(r chi.Router) {
	r.Route("/internal/memory", func(r chi.Router) {
		r.Post("/retrieve", h.retrieve)
	})
}

// MountAdmin registers the admin inspection + probe routes WITHOUT an admin
// gate. The caller wraps these in an auth.RequireAdmin group (see server.go),
// mirroring the beta handler — keeping the gate out of Mount avoids importing
// auth here and the import cycle that would create.
func (h *Handler) MountAdmin(r chi.Router) {
	r.Get("/admin/memories", h.dump)
	r.Post("/admin/memories/search", h.search)
}

// --- internal retrieve --------------------------------------------------

// retrieveRequest is the POST /internal/memory/retrieve body. K and Threshold
// are pointers so an omitted field is distinguishable from an explicit zero:
// omitted threshold means "use the config default", explicit 0 means "no cap".
type retrieveRequest struct {
	UserID    string   `json:"user_id"`
	Query     string   `json:"query"`
	K         *int     `json:"k"`
	Threshold *float64 `json:"threshold"`
}

// retrieve is the agent's recall endpoint. It is best-effort by contract: on
// ANY service failure it logs at warn and returns 200 with an empty memories
// list so a memory outage never fails the agent's turn (SOW: failure / timeout
// / empty ⇒ inject nothing).
func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	var req retrieveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" || req.Query == "" {
		httpresp.Error(w, http.StatusBadRequest, "user_id and query are required")
		return
	}

	k := 0 // 0 ⇒ service applies cfg.TopK
	if req.K != nil {
		k = *req.K
	}
	threshold := -1.0 // omitted ⇒ -1 sentinel ⇒ config default cap
	if req.Threshold != nil {
		threshold = *req.Threshold
	}

	matches, err := h.svc.Retrieve(r.Context(), req.UserID, req.Query, k, threshold)
	if err != nil {
		// Best-effort: never fail the agent's turn on a memory error.
		h.log.WarnContext(r.Context(), "vectormemory retrieve failed, injecting nothing",
			slog.String("user_id", req.UserID),
			slog.Any("error", err),
		)
		httpresp.OK(w, "", map[string]any{"memories": []Match{}})
		return
	}
	if matches == nil {
		matches = []Match{}
	}
	httpresp.OK(w, "", map[string]any{"memories": matches})
}

// --- admin dump ---------------------------------------------------------

const (
	defaultDumpLimit = 100
	maxDumpLimit     = 500
)

// memoryDTO is the admin dump row shape per the SOW. It maps a Memory to the
// JSON the operator inspects; superseded_at is rendered as null (or omitted)
// when the row is still active.
type memoryDTO struct {
	DistilledText   string     `json:"distilled_text"`
	UserID          string     `json:"user_id"`
	SourceSessionID string     `json:"source_session_id"`
	EmbeddingModel  string     `json:"embedding_model"`
	EmbeddingDim    int        `json:"embedding_dim"`
	CreatedAt       time.Time  `json:"created_at"`
	SupersededAt    *time.Time `json:"superseded_at,omitempty"`
}

func (h *Handler) dump(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	limit := parseClampedInt(r.URL.Query().Get("limit"), defaultDumpLimit, 1, maxDumpLimit)
	offset := parseClampedInt(r.URL.Query().Get("offset"), 0, 0, -1)

	rows, err := h.svc.Dump(r.Context(), userID, limit, offset)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "dump agent memories", err)
		return
	}

	dtos := make([]memoryDTO, 0, len(rows))
	for _, m := range rows {
		dtos = append(dtos, memoryDTO{
			DistilledText:   m.DistilledText,
			UserID:          m.UserID,
			SourceSessionID: m.SourceSessionID,
			EmbeddingModel:  m.EmbeddingModel,
			EmbeddingDim:    m.EmbeddingDim,
			CreatedAt:       m.CreatedAt,
			SupersededAt:    m.SupersededAt,
		})
	}
	httpresp.OK(w, "", map[string]any{"memories": dtos})
}

// --- admin search -------------------------------------------------------

// searchRequest is the POST /admin/memories/search body. As with retrieve, K
// and Threshold are pointers so an omitted threshold (config default) differs
// from an explicit 0 (full sweep — the operator probing without a cap).
type searchRequest struct {
	Query     string   `json:"query"`
	UserID    string   `json:"user_id"`
	K         *int     `json:"k"`
	Threshold *float64 `json:"threshold"`
}

// search is the admin probe. It calls the SAME svc.Retrieve the agent uses —
// this is the proof that admin search == agent path — and echoes back the
// active threshold the service actually applied so the operator can see the cap
// in effect.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Query == "" || req.UserID == "" {
		httpresp.Error(w, http.StatusBadRequest, "query and user_id are required")
		return
	}

	k := 0
	if req.K != nil {
		k = *req.K
	}
	// threshold passed to the service uses the -1 sentinel for "config default".
	// activeThreshold is the value the service actually applies: the provided
	// value when present, otherwise the configured default.
	threshold := -1.0
	activeThreshold := h.svc.DefaultThreshold()
	if req.Threshold != nil {
		threshold = *req.Threshold
		activeThreshold = *req.Threshold
	}

	matches, err := h.svc.Retrieve(r.Context(), req.UserID, req.Query, k, threshold)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "search agent memories", err)
		return
	}
	if matches == nil {
		matches = []Match{}
	}
	httpresp.OK(w, "", map[string]any{
		"threshold": activeThreshold,
		"matches":   matches,
	})
}

// parseClampedInt parses s as an int, falling back to def on any parse error,
// then clamps the result to [lo, hi]. A negative hi disables the upper clamp
// (used for offset, which has no ceiling).
func parseClampedInt(s string, def, lo, hi int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		v = def
	}
	if v < lo {
		v = lo
	}
	if hi >= 0 && v > hi {
		v = hi
	}
	return v
}
