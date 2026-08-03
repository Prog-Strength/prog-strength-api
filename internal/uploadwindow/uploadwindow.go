// Package uploadwindow lifts the server's global request deadlines for the
// handful of routes that move whole files through this process.
//
// The API sets ReadTimeout and WriteTimeout to 10s on the shared http.Server
// (internal/server). Those are the right defaults for a JSON API — they are
// what keeps a slow-loris client from pinning a connection — but they apply to
// EVERY route, and three routes cannot live inside them:
//
//   - POST /me/avatar          multipart upload + re-encode + S3 PUT
//   - the TCX upload routes    multipart upload + parse + S3 archive
//
// Activity photos used to be the third and worst case. They no longer are:
// that upload goes browser->S3 through a presigned PUT and the rendering
// happens on a background worker, so nothing about it is bounded by a request
// deadline. See prog-strength-docs/sows/photo-upload-direct-to-s3.md.
//
// ReadTimeout covers reading the entire request INCLUDING the body, and
// WriteTimeout starts when the request headers are read and runs until the
// response is written. So a photo upload's ten seconds has to cover the
// client's transfer, the image pipeline, and the S3 round-trips combined. A
// 6 MB JPEG on a residential upstream blows that budget, and the failure is
// invisible: the server tears the connection down with NO response at all, so
// the browser reports a transport error rather than a status code and no
// status-based metric ever counts it.
//
// This package is a STOPGAP, and photos have already graduated out of it. The
// durable fix for the two routes left is the same one photos took: stop moving
// file bytes through this process at all. When avatar and TCX follow, this
// package goes with them.
package uploadwindow

import (
	"net/http"
	"time"
)

// Window is how long an upload route gets, replacing the server's 10s
// Read/WriteTimeout for that one request.
//
// Two minutes is not a tuning parameter — it is "generously more than any
// legitimate upload needs, and still bounded." At the 32 MiB photo ceiling on a
// slow connection the transfer alone can run well past a minute, and the image
// pipeline adds seconds on two burstable vCPUs. The point is that this deadline
// never fires for real traffic while still capping an abandoned connection.
const Window = 2 * time.Minute

// Extend gives the current request until now+d to finish reading its body and
// writing its response, overriding the server's global Read/WriteTimeout.
// Call it before touching r.Body.
//
// It returns http.ErrNotSupported when the ResponseWriter cannot carry
// deadlines, which off a real *http.Server means httptest.ResponseRecorder —
// i.e. handler unit tests. Callers ignore that: there is no deadline to lift
// when there is no server. Any other error is likewise non-fatal; the request
// simply keeps the global timeout it already had.
func Extend(w http.ResponseWriter, d time.Duration) error {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(d)
	if err := rc.SetReadDeadline(deadline); err != nil {
		return err
	}
	return rc.SetWriteDeadline(deadline)
}
