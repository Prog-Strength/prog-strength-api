package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestWebhookMisrouteNotFound verifies that the NotFound handler increments the
// misroute counter only for paths matching a known provider fragment, leaving
// scanner noise uncounted, and always serves a 404 so 404 behavior is
// unchanged. The counter is a global, so each case captures a before/after
// delta rather than asserting an absolute value.
func TestWebhookMisrouteNotFound(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		wantDelta float64
	}{
		{
			name:      "misrouted whoop webhook increments",
			method:    http.MethodPost,
			path:      "/webhooks/whoop,",
			wantDelta: 1,
		},
		{
			name:      "env scanner noise is not counted",
			method:    http.MethodGet,
			path:      "/.env",
			wantDelta: 0,
		},
		{
			name:      "unrelated provider webhook is not counted as whoop",
			method:    http.MethodPost,
			path:      "/webhooks/incoming/stripe.json",
			wantDelta: 0,
		},
		{
			name:      "matching is case-insensitive",
			method:    http.MethodPost,
			path:      "/Webhooks/WHOOP",
			wantDelta: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := testutil.ToFloat64(webhookMisrouteTotal.WithLabelValues("whoop"))

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			webhookMisrouteNotFound(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}

			after := testutil.ToFloat64(webhookMisrouteTotal.WithLabelValues("whoop"))
			if got := after - before; got != tt.wantDelta {
				t.Errorf("whoop counter delta = %v, want %v", got, tt.wantDelta)
			}
		})
	}
}
