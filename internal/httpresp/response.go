// Package httpresp defines the standard JSON envelopes used by every
// HTTP handler in the API. Centralizing them keeps response shape
// consistent and makes it cheap to add cross-cutting fields (environment,
// version, request ID) in one place.
package httpresp

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/version"
)

const service = "Prog Strength Backend"

// Response is the envelope for successful API responses. Add common
// fields here and they will flow through every handler without
// changing call sites.
//
// RequestID echoes the per-request correlation id minted by the
// requestid middleware (also exposed on the X-Request-ID response
// header). It is omitempty so handlers exercised outside the HTTP
// stack (background jobs, future internal call paths) don't render
// an empty key.
type Response struct {
	Service   string `json:"service"`
	Version   string `json:"version"`
	RequestID string `json:"request_id,omitempty"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
}

// ErrorResponse is the envelope for failed API responses. The HTTP
// status code is the success/failure signal; this body carries a
// human-readable explanation. Error is required; Message is intentionally
// absent so success and failure shapes are unambiguous.
//
// Code is a machine-readable error identifier, introduced for the running
// TCX import client (per the running-tracking SOW) which branches on
// precise reasons — "tcx_not_running", "file_too_large", "duplicate_run".
// It is omitempty so every existing Error() response stays byte-identical:
// only handlers that opt in via ErrorWithCode emit the field.
//
// RequestID matches Response.RequestID — see its doc for rationale.
type ErrorResponse struct {
	Service   string `json:"service"`
	Version   string `json:"version"`
	RequestID string `json:"request_id,omitempty"`
	Error     string `json:"error"`
	Code      string `json:"code,omitempty"`
}

// OK writes a 200 response with the given message and optional data
// (data may be nil and will be omitted from the JSON output).
func OK(w http.ResponseWriter, message string, data any) {
	writeSuccess(w, http.StatusOK, message, data)
}

// Created writes a 201 response. Use after a successful resource creation.
func Created(w http.ResponseWriter, message string, data any) {
	writeSuccess(w, http.StatusCreated, message, data)
}

func writeSuccess(w http.ResponseWriter, status int, message string, data any) {
	writeJSON(w, status, Response{
		Service:   service,
		Version:   version.Version,
		RequestID: w.Header().Get(requestid.HeaderName),
		Message:   message,
		Data:      data,
	})
}

// Error writes a JSON error response with the given status and message.
func Error(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{
		Service:   service,
		Version:   version.Version,
		RequestID: w.Header().Get(requestid.HeaderName),
		Error:     msg,
	})
}

// ErrorWithCode writes a JSON error response carrying a machine-readable
// code alongside the human message. Used by clients (the running import
// flow) that branch on the precise failure reason rather than the prose.
func ErrorWithCode(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, ErrorResponse{
		Service:   service,
		Version:   version.Version,
		RequestID: w.Header().Get(requestid.HeaderName),
		Error:     msg,
		Code:      code,
	})
}

// ErrorWithCodeData writes a coded error envelope plus arbitrary extra
// top-level fields (e.g. the running import's duplicate response carries
// existing_session_id). Building the body here keeps the service/version
// envelope fields in one place rather than re-declaring them at the call
// site. extra keys must not collide with the reserved envelope keys.
func ErrorWithCodeData(w http.ResponseWriter, status int, msg, code string, extra map[string]any) {
	body := map[string]any{
		"service": service,
		"version": version.Version,
		"error":   msg,
		"code":    code,
	}
	if rid := w.Header().Get(requestid.HeaderName); rid != "" {
		body["request_id"] = rid
	}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

// ServerError logs op and err for operators, then writes a generic 500
// to avoid leaking internal details to callers. ctx is reserved for
// structured logging (request ID, user ID) once log/slog is adopted.
func ServerError(w http.ResponseWriter, ctx context.Context, op string, err error) {
	log.Printf("%s: %v", op, err)
	Error(w, http.StatusInternalServerError, "internal server error")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Headers are already sent; log and move on.
		log.Printf("write json: %v", err)
	}
}
