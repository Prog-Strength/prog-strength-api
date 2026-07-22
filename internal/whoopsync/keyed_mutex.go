package whoopsync

import "sync"

// keyedMutex provides per-key mutual exclusion: Lock(key) blocks only against
// other holders of the SAME key, so concurrent syncs for different users never
// contend. It is used to serialize the token-refresh critical section per user
// (single-use refresh rotation must not run twice concurrently for one user).
//
// The zero value is not usable; build one with newKeyedMutex.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// newKeyedMutex builds a ready-to-use keyedMutex.
func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*sync.Mutex)}
}

// Lock acquires the mutex for key, creating it on first use. It blocks until
// the per-key mutex is free.
func (k *keyedMutex) Lock(key string) {
	k.mu.Lock()
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
}

// Unlock releases the mutex for key. It panics if key was never locked, matching
// sync.Mutex's unlock-of-unlocked behavior.
func (k *keyedMutex) Unlock(key string) {
	k.mu.Lock()
	m := k.locks[key]
	k.mu.Unlock()
	if m == nil {
		panic("whoopsync: Unlock of unlocked keyedMutex key " + key)
	}
	m.Unlock()
}
