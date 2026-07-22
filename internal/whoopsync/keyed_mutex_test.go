package whoopsync

import (
	"sync"
	"testing"
)

// TestKeyedMutex_SerializesSameKey verifies concurrent holders of the same key
// never overlap: with proper mutual exclusion, incrementing a shared counter
// under Lock(key) from many goroutines yields no lost updates.
func TestKeyedMutex_SerializesSameKey(t *testing.T) {
	km := newKeyedMutex()
	const goroutines, iters = 20, 500
	counter := 0

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				km.Lock("k")
				counter++
				km.Unlock("k")
			}
		}()
	}
	wg.Wait()

	if want := goroutines * iters; counter != want {
		t.Fatalf("counter = %d, want %d (lost updates → not mutually exclusive)", counter, want)
	}
}

// TestKeyedMutex_DifferentKeysIndependent verifies two different keys do not
// block each other: holding key "a" must not prevent locking key "b".
func TestKeyedMutex_DifferentKeysIndependent(t *testing.T) {
	km := newKeyedMutex()
	km.Lock("a")
	defer km.Unlock("a")

	done := make(chan struct{})
	go func() {
		km.Lock("b")
		km.Unlock("b")
		close(done)
	}()

	<-done // would deadlock/block if "b" contended with the held "a".
}
