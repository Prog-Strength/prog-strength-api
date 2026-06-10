package user

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// FakeAvatarStore is an in-memory AvatarStore for tests. PresignGet returns a
// deterministic sentinel URL so handler tests can assert the resolved
// avatar_url without hitting S3; tagged keys are recorded for assertions.
type FakeAvatarStore struct {
	mu       sync.Mutex
	objects  map[string][]byte
	tagged   map[string]bool
	putErr   error
	signErr  error
	tagErr   error
	tagCalls int
}

// Compile-time check that *FakeAvatarStore satisfies AvatarStore.
var _ AvatarStore = (*FakeAvatarStore)(nil)

func NewFakeAvatarStore() *FakeAvatarStore {
	return &FakeAvatarStore{
		objects: make(map[string][]byte),
		tagged:  make(map[string]bool),
	}
}

func (f *FakeAvatarStore) Put(ctx context.Context, key, contentType string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.objects[key] = body
	return nil
}

func (f *FakeAvatarStore) PresignGet(ctx context.Context, key string) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}
	return "https://signed.example/" + key, nil
}

func (f *FakeAvatarStore) TagOrphaned(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagCalls++
	if f.tagErr != nil {
		return f.tagErr
	}
	f.tagged[key] = true
	return nil
}

func (f *FakeAvatarStore) wasTagged(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tagged[key]
}

func (f *FakeAvatarStore) tagCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tagCalls
}

func TestAvatarKey_Format(t *testing.T) {
	key := AvatarKey("abc123", "png")
	if !strings.HasPrefix(key, "user_id=abc123/") {
		t.Fatalf("key missing user partition prefix: %q", key)
	}
	if !strings.HasSuffix(key, ".png") {
		t.Fatalf("key missing extension: %q", key)
	}
	// The random component should differ between calls.
	if AvatarKey("abc123", "png") == key {
		t.Fatal("expected distinct random components per call")
	}
}

func TestExtForContentType(t *testing.T) {
	cases := map[string]struct {
		wantExt string
		wantOK  bool
	}{
		"image/png":       {"png", true},
		"image/jpeg":      {"jpg", true},
		"image/webp":      {"webp", true},
		"text/plain":      {"", false},
		"application/pdf": {"", false},
		"image/gif":       {"", false},
	}
	for ct, want := range cases {
		ext, ok := ExtForContentType(ct)
		if ext != want.wantExt || ok != want.wantOK {
			t.Errorf("ExtForContentType(%q) = (%q, %v), want (%q, %v)", ct, ext, ok, want.wantExt, want.wantOK)
		}
	}
}

func TestFakeAvatarStore_TagOrphanedRecordsKey(t *testing.T) {
	f := NewFakeAvatarStore()
	if err := f.TagOrphaned(context.Background(), "user_id=u1/old.png"); err != nil {
		t.Fatalf("TagOrphaned: %v", err)
	}
	if !f.wasTagged("user_id=u1/old.png") {
		t.Fatal("expected key to be recorded as tagged")
	}
}
