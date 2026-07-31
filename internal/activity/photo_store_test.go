package activity

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// newTestPresigner builds a windowedPresigner with static credentials and a
// controllable clock so the presign tests need no network and no real AWS
// config.
func newTestPresigner(window time.Duration, now func() time.Time) *windowedPresigner {
	return &windowedPresigner{
		creds: aws.Credentials{
			AccessKeyID:     "AKID",
			SecretAccessKey: "SECRET",
		},
		signer: v4.NewSigner(),
		region: "us-east-1",
		bucket: "test-bucket",
		window: window,
		now:    now,
	}
}

// FakePhotoStore is an in-memory PhotoStore for hermetic handler tests. It
// records every successful Put, returns a deterministic presigned URL, and
// records orphaned keys. PutFunc lets a test force a Put to fail on a chosen
// call (e.g. make the 2nd Put fail); it receives the 1-based call number.
type FakePhotoStore struct {
	mu       sync.Mutex
	Puts     map[string][]byte
	Orphaned []string
	PutCount int
	PutFunc  func(callN int) error
}

// Compile-time check that *FakePhotoStore satisfies PhotoStore.
var _ PhotoStore = (*FakePhotoStore)(nil)

// NewFakePhotoStore returns a ready-to-use in-memory PhotoStore.
func NewFakePhotoStore() *FakePhotoStore {
	return &FakePhotoStore{Puts: make(map[string][]byte)}
}

func (f *FakePhotoStore) Put(_ context.Context, key, _ string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PutCount++
	if f.PutFunc != nil {
		if err := f.PutFunc(f.PutCount); err != nil {
			return err
		}
	}
	// Copy so a caller mutating body after Put can't corrupt our record.
	stored := make([]byte, len(body))
	copy(stored, body)
	f.Puts[key] = stored
	return nil
}

func (f *FakePhotoStore) PresignGet(_ context.Context, key string) (string, error) {
	return "https://fake/" + key, nil
}

func (f *FakePhotoStore) TagOrphaned(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Orphaned = append(f.Orphaned, key)
	return nil
}

func expiresParam(t *testing.T, signed string) string {
	t.Helper()
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	return u.Query().Get("X-Amz-Expires")
}

// Two presigns one second apart but within the same window must produce a
// byte-identical URL so the browser treats them as one cacheable resource.
func TestPresignWindowStableWithinWindow(t *testing.T) {
	window := 15 * time.Minute
	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	now := base
	p := newTestPresigner(window, func() time.Time { return now })

	u1, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign 1: %v", err)
	}

	now = base.Add(1 * time.Second)
	u2, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign 2: %v", err)
	}

	if u1 != u2 {
		t.Fatalf("expected byte-identical URLs within window, got:\n  %s\n  %s", u1, u2)
	}
	t.Logf("in-window URL (identical for both calls):\n%s", u1)
}

// Two presigns straddling a window boundary must produce different URLs
// (a fresh signature keyed to the new window's signing time).
func TestPresignWindowChangesAcrossBoundary(t *testing.T) {
	window := 15 * time.Minute
	// 11:59:59 and 12:00:01 truncate to different 15-minute windows only if the
	// boundary at 12:00:00 sits between them — pick times that straddle 12:00.
	before := time.Date(2026, 7, 31, 11, 59, 59, 0, time.UTC)
	after := time.Date(2026, 7, 31, 12, 0, 1, 0, time.UTC)

	now := before
	p := newTestPresigner(window, func() time.Time { return now })

	u1, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign 1: %v", err)
	}

	now = after
	u2, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign 2: %v", err)
	}

	if u1 == u2 {
		t.Fatalf("expected different URLs across window boundary, both were:\n%s", u1)
	}
}

// X-Amz-Expires must be 2*window expressed in seconds.
func TestPresignExpiresIsTwoWindows(t *testing.T) {
	window := 15 * time.Minute
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	p := newTestPresigner(window, func() time.Time { return now })

	u, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	got := expiresParam(t, u)
	want := "1800" // 2 * 15 minutes = 1800 seconds
	if got != want {
		t.Fatalf("X-Amz-Expires = %q, want %q", got, want)
	}
}

// The signed URL should escape key path segments while preserving the "/"
// separators of the Hive-partitioned key.
func TestPresignPreservesKeySeparators(t *testing.T) {
	window := 15 * time.Minute
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	p := newTestPresigner(window, func() time.Time { return now })

	key := "user_id=u1/activity_type=run/variant=full/photo1.jpg"
	u, err := p.presignGet(context.Background(), key)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Path != "/"+key {
		t.Fatalf("path = %q, want %q", parsed.Path, "/"+key)
	}
}

func TestFakePhotoStoreRecordsPutsAndOrphans(t *testing.T) {
	f := NewFakePhotoStore()
	ctx := context.Background()

	if err := f.Put(ctx, "k1", "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("put k1: %v", err)
	}
	if err := f.Put(ctx, "k2", "image/jpeg", []byte("bb")); err != nil {
		t.Fatalf("put k2: %v", err)
	}
	if got := string(f.Puts["k1"]); got != "a" {
		t.Fatalf("put k1 body = %q, want %q", got, "a")
	}
	if got := string(f.Puts["k2"]); got != "bb" {
		t.Fatalf("put k2 body = %q, want %q", got, "bb")
	}

	url, err := f.PresignGet(ctx, "k1")
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if url != "https://fake/k1" {
		t.Fatalf("presign url = %q", url)
	}

	if err := f.TagOrphaned(ctx, "k1"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if len(f.Orphaned) != 1 || f.Orphaned[0] != "k1" {
		t.Fatalf("orphaned = %v, want [k1]", f.Orphaned)
	}
}

func TestFakePhotoStorePutFuncForcesFailure(t *testing.T) {
	wantErr := errors.New("boom")
	f := NewFakePhotoStore()
	f.PutFunc = func(callN int) error {
		if callN == 2 {
			return wantErr
		}
		return nil
	}
	ctx := context.Background()

	if err := f.Put(ctx, "k1", "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("first put should succeed, got %v", err)
	}
	if err := f.Put(ctx, "k2", "image/jpeg", []byte("b")); !errors.Is(err, wantErr) {
		t.Fatalf("second put err = %v, want %v", err, wantErr)
	}
	// A failed put must not be recorded.
	if _, ok := f.Puts["k2"]; ok {
		t.Fatalf("failed put should not be recorded")
	}
	if f.PutCount != 2 {
		t.Fatalf("PutCount = %d, want 2", f.PutCount)
	}
}
