package activity

import (
	"context"
	"sync"
)

// MemoryArchiver is the dev/test Archiver. It keeps objects (with their
// metadata) in a map so tests can assert what the repository wrote, and
// PutErr lets a test force a storage failure to exercise the Create
// rollback path.
type MemoryArchiver struct {
	mu      sync.Mutex
	objects map[string]storedObject
	// PutErr, when non-nil, is returned from every Put without storing
	// anything — used to simulate an S3 outage in tests.
	PutErr error
}

type storedObject struct {
	body []byte
	meta ObjectMetadata
}

// Compile-time check that *MemoryArchiver satisfies Archiver.
var _ Archiver = (*MemoryArchiver)(nil)

func NewMemoryArchiver() *MemoryArchiver {
	return &MemoryArchiver{objects: make(map[string]storedObject)}
}

func (a *MemoryArchiver) Put(ctx context.Context, key string, body []byte, meta ObjectMetadata) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.PutErr != nil {
		return a.PutErr
	}
	// Defensive copy so a later mutation of the caller's slice can't
	// rewrite stored bytes.
	stored := make([]byte, len(body))
	copy(stored, body)
	a.objects[key] = storedObject{body: stored, meta: meta}
	return nil
}

func (a *MemoryArchiver) Delete(ctx context.Context, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.objects, key)
	return nil
}

// Get returns a copy of the stored bytes for key, or ErrNotFound when no
// object is stored. Satisfies the Archiver interface (used by the
// best-efforts backfill); the copy keeps callers from mutating internal
// state.
func (a *MemoryArchiver) Get(ctx context.Context, key string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, ok := a.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(o.body))
	copy(cp, o.body)
	return cp, nil
}

// Meta returns the metadata stored alongside key and whether it exists.
// For test assertions on the ingest-source tag.
func (a *MemoryArchiver) Meta(key string) (ObjectMetadata, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, ok := a.objects[key]
	if !ok {
		return ObjectMetadata{}, false
	}
	return o.meta, true
}

// Len reports how many objects are stored. For test assertions.
func (a *MemoryArchiver) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.objects)
}
