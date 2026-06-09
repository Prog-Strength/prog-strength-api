package httpresp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// withRequestID writes the X-Request-ID header onto a fresh recorder.
// The middleware does this in prod; tests do it directly.
func withRequestID(id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	if id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestError_IncludesRequestIDFromResponseHeader(t *testing.T) {
	const rid = "abc123def456"
	w := withRequestID(rid)

	httpresp.Error(w, 400, "nope")

	body := decodeBody(t, w)
	if got := body["request_id"]; got != rid {
		t.Fatalf("body request_id = %v, want %q", got, rid)
	}
}

func TestError_OmitsRequestIDWhenAbsent(t *testing.T) {
	w := httptest.NewRecorder()

	httpresp.Error(w, 400, "nope")

	body := decodeBody(t, w)
	if _, ok := body["request_id"]; ok {
		t.Fatalf("body unexpectedly contained request_id: %v", body)
	}
}

func TestOK_IncludesRequestIDFromResponseHeader(t *testing.T) {
	const rid = "ok-id-xyz"
	w := withRequestID(rid)

	httpresp.OK(w, "fetched", map[string]string{"foo": "bar"})

	body := decodeBody(t, w)
	if got := body["request_id"]; got != rid {
		t.Fatalf("body request_id = %v, want %q", got, rid)
	}
}

func TestErrorWithCode_IncludesRequestID(t *testing.T) {
	const rid = "ewc-id-1"
	w := withRequestID(rid)

	httpresp.ErrorWithCode(w, 500, "boom", "storage_failed")

	body := decodeBody(t, w)
	if got := body["request_id"]; got != rid {
		t.Fatalf("body request_id = %v, want %q", got, rid)
	}
	if got := body["code"]; got != "storage_failed" {
		t.Fatalf("body code = %v, want storage_failed", got)
	}
}

func TestErrorWithCodeData_IncludesRequestID(t *testing.T) {
	const rid = "ewcd-id-1"
	w := withRequestID(rid)

	httpresp.ErrorWithCodeData(w, 409, "dup", "duplicate_run", map[string]any{
		"existing_session_id": "sess_abc",
	})

	body := decodeBody(t, w)
	if got := body["request_id"]; got != rid {
		t.Fatalf("body request_id = %v, want %q", got, rid)
	}
	if got := body["existing_session_id"]; got != "sess_abc" {
		t.Fatalf("extra field lost: %v", got)
	}
}

// TestServerError_GenericMessage guards against accidentally leaking the
// underlying error to the client when ServerError is used. The original
// op + err must be logged, but the response body stays generic. We test
// the body-side; log capture lives elsewhere.
func TestServerError_GenericMessage(t *testing.T) {
	w := httptest.NewRecorder()

	httpresp.ServerError(w, context.Background(), "create thing", errors.New("internal detail leaked"))

	body := decodeBody(t, w)
	if got := body["error"]; got != "internal server error" {
		t.Fatalf("body error = %v, want generic message", got)
	}
}
