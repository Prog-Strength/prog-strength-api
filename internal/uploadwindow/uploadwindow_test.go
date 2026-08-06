package uploadwindow_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/uploadwindow"
)

// serverTimeout is the stand-in for the API's global 10s Read/WriteTimeout,
// scaled down so the tests stay fast.
const serverTimeout = 300 * time.Millisecond

// serve starts a real *http.Server — not httptest.NewServer — because these
// tests are about the server's Read/WriteTimeout, which httptest does not set.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:      h,
		ReadTimeout:  serverTimeout,
		WriteTimeout: serverTimeout,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

// slowBody emits chunks spaced `gap` apart — a client on a modest upstream
// pushing a multipart body.
type slowBody struct {
	chunks, sent int
	gap          time.Duration
}

func (s *slowBody) Read(p []byte) (int, error) {
	if s.sent >= s.chunks {
		return 0, io.EOF
	}
	time.Sleep(s.gap)
	s.sent++
	return copy(p, make([]byte, 1024)), nil
}

// A handler doing work that outlasts WriteTimeout — processPhoto plus two S3
// PUTs — still gets to write its response.
func TestExtendLetsSlowWorkOutliveWriteTimeout(t *testing.T) {
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadwindow.Extend(w, 5*time.Second)
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(serverTimeout * 3)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("done"))
	}))

	resp, err := http.Post(url, "multipart/form-data", &slowBody{chunks: 1})
	if err != nil {
		t.Fatalf("request failed, want a response: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if string(body) != "done" {
		t.Errorf("body = %q, want %q", body, "done")
	}
}

// A request body still arriving when ReadTimeout would have fired is read to
// completion — the 6 MB-on-a-slow-upstream case.
func TestExtendLetsSlowBodyOutliveReadTimeout(t *testing.T) {
	var readErr error
	var readBytes int
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadwindow.Extend(w, 5*time.Second)
		var b []byte
		b, readErr = io.ReadAll(r.Body)
		readBytes = len(b)
		w.WriteHeader(http.StatusCreated)
	}))

	// 6 chunks x 100ms = 600ms of upload against a 300ms ReadTimeout.
	req, err := http.NewRequest(http.MethodPost, url, &slowBody{chunks: 6, gap: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed, want a response: %v", err)
	}
	defer resp.Body.Close()

	if readErr != nil {
		t.Errorf("server-side body read err = %v, want nil", readErr)
	}
	if readBytes != 6*1024 {
		t.Errorf("read %d bytes, want %d", readBytes, 6*1024)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
}

// Guard rail for the whole exercise: without Extend the same slow handler is
// killed. If this ever passes, the global timeouts stopped applying and the
// tests above prove nothing.
func TestWithoutExtendSlowWorkIsKilled(t *testing.T) {
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(serverTimeout * 3)
		w.WriteHeader(http.StatusCreated)
	}))

	resp, err := http.Post(url, "multipart/form-data", &slowBody{chunks: 1})
	if err == nil {
		resp.Body.Close()
		t.Fatalf("got status %d, want a transport error from WriteTimeout", resp.StatusCode)
	}
}

// Off a real server — httptest.ResponseRecorder, which every handler unit test
// in this repo uses — Extend must degrade quietly rather than panic, and must
// report that deadlines were unsupported.
func TestExtendReportsUnsupportedOnRecorder(t *testing.T) {
	err := uploadwindow.Extend(httptest.NewRecorder(), time.Minute)
	if !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("err = %v, want http.ErrNotSupported", err)
	}
}
