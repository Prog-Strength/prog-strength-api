package whoopsync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// Header names WHOOP sets on every webhook delivery. The signature is the
// base64 HMAC-SHA256 of (timestamp + raw body), keyed by the app's client
// secret; the timestamp guards against replay of a captured delivery.
const (
	sigHeader = "X-WHOOP-Signature"
	tsHeader  = "X-WHOOP-Signature-Timestamp"
)

// maxWebhookBody bounds the request body we will read (gosec G120). WHOOP
// deliveries are tiny JSON objects; 1 MiB is generous headroom.
const maxWebhookBody = 1 << 20

// webhookSkew is how far the signed timestamp may drift from our clock (either
// direction) before we reject the delivery as stale / replayed.
const webhookSkew = 5 * time.Minute

// webhookSyncLimit is the small list limit handed to SyncWindow for a
// webhook-triggered recent sync (covers the handful of recoveries a nudge is
// about; see Service.SyncWindow).
const webhookSyncLimit = 10

// webhookSyncer is the subset of *Service the webhook needs. Defining it here
// (rather than depending on *Service) lets tests inject a fake, mirroring how
// service.go narrows its dependencies.
type webhookSyncer interface {
	SyncWindow(ctx context.Context, userID string, limit int) error
}

// Compile-time check that the production *Service satisfies webhookSyncer.
var _ webhookSyncer = (*Service)(nil)

// WebhookHandler serves WHOOP's public recovery webhook. It is mounted OUTSIDE
// the JWT-gated group (WHOOP calls it directly), so the only authentication is
// the HMAC signature verified against the app's client secret.
type WebhookHandler struct {
	secret []byte // WHOOP client secret — the HMAC key
	conns  whoopconn.Repository
	rec    whooprecovery.Repository
	svc    webhookSyncer
	now    func() time.Time
}

// NewWebhookHandler wires the webhook handler. secret is the WHOOP client
// secret used as the HMAC key. now defaults to time.Now when nil.
func NewWebhookHandler(secret []byte, conns whoopconn.Repository, rec whooprecovery.Repository, svc webhookSyncer, now func() time.Time) *WebhookHandler {
	if now == nil {
		now = time.Now
	}
	return &WebhookHandler{
		secret: secret,
		conns:  conns,
		rec:    rec,
		svc:    svc,
		now:    now,
	}
}

// Mount registers the public webhook route. Mount this OUTSIDE the JWT-gated
// group — the signature is the only credential.
func (h *WebhookHandler) Mount(r chi.Router) {
	r.Post("/webhooks/whoop", h.handle)
}

// webhookEvent is the subset of WHOOP's webhook payload we act on. In v2 `id`
// is the sleep UUID string; `user_id` is WHOOP's numeric account id used to
// route the event back to a local user.
type webhookEvent struct {
	UserID int64  `json:"user_id"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

// handle verifies the HMAC signature, routes the event to the owning user, and
// runs the sync/delete inline. Response codes are chosen so WHOOP's 5× retry
// helps on transient failures (500) but never fires for events we deliberately
// drop (204): a bad signature is 401, an event for an unknown/disconnected user
// is 204, and a genuine downstream failure is 500.
func (h *WebhookHandler) handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if !h.verify(r.Header.Get(sigHeader), r.Header.Get(tsHeader), body) {
		slog.WarnContext(ctx, "whoop webhook: signature verification failed")
		webhooksTotal.WithLabelValues("invalid", "bad_signature").Inc()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var event webhookEvent
	if err = json.Unmarshal(body, &event); err != nil {
		// Signature already verified, so this is a malformed-but-authentic body.
		slog.WarnContext(ctx, "whoop webhook: bad json after valid signature", "error", err)
		webhooksTotal.WithLabelValues("invalid", "bad_json").Inc()
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	conn, err := h.conns.GetByWhoopUserID(ctx, event.UserID)
	if err != nil {
		if errors.Is(err, whoopconn.ErrNotFound) {
			// No local user for this WHOOP account — drop. Returning a non-2xx
			// would make WHOOP retry an event we never want. Logged (not
			// silent): a steady stream of these means a connection row was
			// lost while the WHOOP-side registration lives on.
			slog.InfoContext(ctx, "whoop webhook: dropped, no local user",
				"type", event.Type, "whoop_user_id", event.UserID)
			webhooksTotal.WithLabelValues(event.Type, "unknown_user").Inc()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		slog.ErrorContext(ctx, "whoop webhook: route by whoop user id", "error", err)
		webhooksTotal.WithLabelValues(event.Type, "route_error").Inc()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if conn.Status != whoopconn.StatusConnected {
		// Revoked / error connection: drop, don't retry.
		slog.InfoContext(ctx, "whoop webhook: dropped, connection not active",
			"type", event.Type, "user_id", conn.UserID, "status", conn.Status)
		webhooksTotal.WithLabelValues(event.Type, "not_connected").Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch event.Type {
	case "recovery.updated":
		if err := h.svc.SyncWindow(ctx, conn.UserID, webhookSyncLimit); err != nil {
			// Transient sync failure: return 500 so WHOOP's retry gets another
			// shot. SyncWindow is idempotent, so a later retry is safe.
			slog.ErrorContext(ctx, "whoop webhook: sync window", "user_id", conn.UserID, "error", err)
			webhooksTotal.WithLabelValues(event.Type, "sync_error").Inc()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// The sync itself logged its row counts; this line is the delivery's
		// outcome, so one Logs Insights filter shows the webhook lifecycle.
		slog.InfoContext(ctx, "whoop webhook: handled",
			"type", event.Type, "user_id", conn.UserID)
		webhooksTotal.WithLabelValues(event.Type, "synced").Inc()
	case "recovery.deleted":
		if err := h.rec.DeleteBySleepID(ctx, conn.UserID, event.ID); err != nil {
			slog.ErrorContext(ctx, "whoop webhook: delete recovery", "user_id", conn.UserID, "sleep_id", event.ID, "error", err)
			webhooksTotal.WithLabelValues(event.Type, "delete_error").Inc()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		slog.InfoContext(ctx, "whoop webhook: handled",
			"type", event.Type, "user_id", conn.UserID, "sleep_id", event.ID)
		webhooksTotal.WithLabelValues(event.Type, "deleted").Inc()
	default:
		// Event type we don't handle — accept and drop. Debug (not info): a
		// broad WHOOP-side subscription would make this line very chatty.
		slog.DebugContext(ctx, "whoop webhook: ignored event type",
			"type", event.Type, "user_id", conn.UserID)
		webhooksTotal.WithLabelValues(event.Type, "ignored").Inc()
	}
	w.WriteHeader(http.StatusNoContent)
}

// verify checks WHOOP's HMAC signature and timestamp freshness. It returns true
// only when both headers are present, the timestamp is a millisecond epoch
// within ±webhookSkew of now, and the constant-time comparison of the computed
// and provided signatures matches.
func (h *WebhookHandler) verify(sig, ts string, body []byte) bool {
	if sig == "" || ts == "" {
		return false
	}

	millis, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	delta := h.now().Sub(time.UnixMilli(millis))
	if delta < -webhookSkew || delta > webhookSkew {
		return false
	}

	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(ts))
	mac.Write(body)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(want))
}
