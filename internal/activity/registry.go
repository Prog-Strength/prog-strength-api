package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
)

// Descriptor is everything the base domain needs to know about one activity
// type. Adding a new type = implement a Descriptor + register it in server.go
// (+ a detail-table migration only if the type has unique structured metrics).
// See prog-strength-docs: "adding an activity type" recipe.
type Descriptor struct {
	Type ActivityType

	// ValidateCreate checks the type-specific rules of a create/update
	// payload before any write (e.g. "runs require distance"). Base-field
	// validation (start time present, known type) is the unified handler's
	// job, not the descriptor's. Optional: nil means the type has no rules
	// of its own and the handler skips the check.
	ValidateCreate func(req CreateRequest) error

	// Details loads/saves the type's detail representation. Nil for
	// base-only types (e.g. a future kickboxing) whose whole state fits
	// the activities base row.
	Details DetailStore

	// Summarize renders the list/card summary for feeds and unified lists.
	// details is whatever the type's DetailStore.Load returned (nil for
	// base-only types or when the caller only has the base row). There is
	// no error return: a nil or mistyped details value deliberately
	// degrades to a base-row-only render rather than failing the page.
	// Optional: nil means the type has no card — callers omit the item's
	// summary instead of invoking it (see RenderSummary).
	Summarize func(a Activity, details any) Summary

	// DecodeDetails parses a (ValidateCreate-approved) Details blob into the
	// typed value DetailStore.Save accepts and Summarize renders. Returns
	// nil, nil for an absent/JSON-null blob. Nil for base-only types. Only
	// the generic create/update path uses it — types with their own Create/
	// Update funcs decode inside those.
	DecodeDetails func(raw json.RawMessage) (any, error)

	// Create, optional: persists a brand-new session of this type when a
	// bare base-row insert + Details.Save can't express the type's create.
	// Strength supplies it so the write runs through the workout repository
	// (PR detection, 1RM history) and the same best-effort side effects
	// (timeline posts, plan matching) as POST /workouts — the descriptor is
	// bound to the workout handler's machinery in server.go rather than
	// reimplementing it. Nil: the unified handler inserts the base row via
	// Repository.CreateManual and saves decoded details.
	Create func(ctx context.Context, userID string, req CreateRequest) (activityID string, err error)

	// Update, optional: full-replacement update counterpart of Create, for
	// PUT /activities/{id}. Strength supplies it so an edit recomputes PRs
	// and leaves TCX-enrichment vitals untouched, exactly like PUT
	// /workouts/{id}. Nil: the unified handler updates the base row via
	// Repository.UpdateBase and replaces details through the DetailStore.
	Update func(ctx context.Context, userID, activityID string, req CreateRequest) error

	// MountRoutes registers type-specific endpoints on the /activities
	// subrouter (nil when none). Wired in server.go, mounted by
	// Handler.Mount.
	MountRoutes func(r chi.Router)
}

// DetailStore persists one type's detail representation, keyed by the base
// activity id. Load/Save/Delete enforce ownership like every repository in
// this codebase: a missing/soft-deleted activity and a wrong userID both
// read as this package's ErrNotFound — one uniform sentinel across ALL
// implementations, so stores backed by another package's repository must
// translate their own not-found at the seam. The `any` payload is the
// type's own detail struct — the base domain never inspects it, only routes
// it between the store and Summarize.
type DetailStore interface {
	Load(ctx context.Context, userID, activityID string) (any, error)
	Save(ctx context.Context, userID, activityID string, details any) error
	Delete(ctx context.Context, userID, activityID string) error
}

// BulkDetailLoader is an optional DetailStore capability: load details for
// many of one user's activities in a single round trip. The unified list
// uses it both to render Summaries without a per-row query fan-out AND to
// populate the wire-visible per-item Details payload (stage-3 /workouts
// parity), so LoadMany must return the full detail payload, not a
// summary-only projection. Stores that don't implement it are summarized
// from the base row alone (which is enough for the endurance types — their
// list distance is joined onto the base read). ids must already be
// user-scoped: they come from the same user's base list read, so
// implementations may skip per-id ownership checks. Ids that don't resolve
// are simply absent from the map.
type BulkDetailLoader interface {
	LoadMany(ctx context.Context, userID string, ids []string) (map[string]any, error)
}

// CreateRequest is the type-agnostic create/update payload the unified
// POST/PUT /activities surface will accept: the activities base columns plus
// the type-specific Details blob, which each descriptor's ValidateCreate
// parses and checks. Deliberately minimal — the handler (next task) owns
// decoding HTTP into this shape.
type CreateRequest struct {
	Type            ActivityType
	StartTime       time.Time
	DurationSeconds *int
	Name            *string
	Notes           *string
	AvgHeartRateBpm *int
	MaxHeartRateBpm *int
	TotalCalories   *int
	Details         json.RawMessage
}

// ErrUnknownActivityType is returned by Registry.Lookup for a type no
// descriptor was registered for. The handler maps it to a 422 listing the
// valid types.
var ErrUnknownActivityType = fmt.Errorf("activity: unknown activity type")

// Registry is the closed set of activity types the system knows how to
// validate, persist details for, and summarize. Built once at boot in
// server.go; the map is never mutated afterwards, so lookups are safe for
// concurrent use.
type Registry struct {
	byType map[ActivityType]*Descriptor
}

// NewRegistry builds a registry from the given descriptors. A duplicate
// Type is a wiring bug in server.go, so it panics — better to fail the boot
// than to silently let one descriptor shadow another.
func NewRegistry(ds ...*Descriptor) *Registry {
	r := &Registry{byType: make(map[ActivityType]*Descriptor, len(ds))}
	for _, d := range ds {
		if _, dup := r.byType[d.Type]; dup {
			panic(fmt.Sprintf("activity: duplicate descriptor registered for type %q", d.Type))
		}
		r.byType[d.Type] = d
	}
	return r
}

// Lookup returns the descriptor for t, or ErrUnknownActivityType naming the
// offending value and the valid set (so the API's 422 body writes itself).
func (r *Registry) Lookup(t ActivityType) (*Descriptor, error) {
	d, ok := r.byType[t]
	if !ok {
		return nil, fmt.Errorf("%w: %q (valid types: %v)", ErrUnknownActivityType, t, r.Types())
	}
	return d, nil
}

// Types returns the registered type names sorted lexically, for validation
// error messages and stable API output.
func (r *Registry) Types() []ActivityType {
	out := make([]ActivityType, 0, len(r.byType))
	for t := range r.byType {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Descriptors returns every registered descriptor in Types() order, for
// callers that iterate the whole set (route mounting).
func (r *Registry) Descriptors() []*Descriptor {
	types := r.Types()
	out := make([]*Descriptor, 0, len(types))
	for _, t := range types {
		out = append(out, r.byType[t])
	}
	return out
}
