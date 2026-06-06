package running

import (
	"context"
	"sync"
)

// MemoryArchiver is the dev/test Archiver. It keeps objects in a map so
// tests can assert what the repository wrote, and PutErr lets a test force
// a storage failure to exercise the Create rollback path.
type MemoryArchiver struct {
	mu      sync.Mutex
	objects map[string][]byte
	// PutErr, when non-nil, is returned from every Put without storing
	// anything — used to simulate an S3 outage in tests.
	PutErr error
}

// Compile-time check that *MemoryArchiver satisfies Archiver.
var _ Archiver = (*MemoryArchiver)(nil)

func NewMemoryArchiver() *MemoryArchiver {
	return &MemoryArchiver{objects: make(map[string][]byte)}
}

func (a *MemoryArchiver) Put(ctx context.Context, key string, body []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.PutErr != nil {
		return a.PutErr
	}
	// Defensive copy so a later mutation of the caller's slice can't
	// rewrite stored bytes.
	stored := make([]byte, len(body))
	copy(stored, body)
	a.objects[key] = stored
	return nil
}

func (a *MemoryArchiver) Delete(ctx context.Context, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.objects, key)
	return nil
}

// Get returns the stored bytes for key and whether it exists. For test
// assertions; returns a copy so callers can't mutate internal state.
func (a *MemoryArchiver) Get(key string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.objects[key]
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, true
}

// Len reports how many objects are stored. For test assertions.
func (a *MemoryArchiver) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.objects)
}
