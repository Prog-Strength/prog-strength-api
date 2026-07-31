package activity

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// newTestPresigner builds a windowedPresigner with static credentials and a
// controllable clock so the presign tests need no network and no real AWS
// config.
func newTestPresigner(window time.Duration, now func() time.Time) *windowedPresigner {
	return &windowedPresigner{
		creds:  credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		signer: v4.NewSigner(),
		region: "us-east-1",
		bucket: "test-bucket",
		window: window,
		now:    now,
	}
}

// rotatingCredentialsProvider hands out each credential set in turn, then keeps
// returning the last one. It stands in for the SDK's credential cache sitting
// on top of IMDS: same provider, different credentials over time.
type rotatingCredentialsProvider struct {
	mu     sync.Mutex
	calls  int
	issued []aws.Credentials
}

func (p *rotatingCredentialsProvider) Retrieve(context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.issued[min(p.calls, len(p.issued)-1)]
	p.calls++
	return c, nil
}

// callCount reports how many times Retrieve was called.
func (p *rotatingCredentialsProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// failingCredentialsProvider always fails to resolve, standing in for IMDS
// being unreachable.
type failingCredentialsProvider struct{ err error }

func (p failingCredentialsProvider) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, p.err
}

// THE EXPIRED-TOKEN REGRESSION.
//
// The API signs presigned GETs with the EC2 instance role's credentials, and
// those are TEMPORARY: IMDS issues a session token good for a few hours and the
// SDK's credential cache silently rotates it. This presigner used to snapshot
// an aws.Credentials VALUE once, at store construction, so every URL minted
// after the first rotation carried a dead X-Amz-Security-Token. S3 answered
// 403 ExpiredToken and every activity photo and video on the site rendered as
// a broken image until the API happened to be redeployed — invisible in the API
// logs, because signing itself never fails.
//
// So: credentials must be resolved on EVERY presign, through the provider.
func TestPresignGetPicksUpRotatedCredentials(t *testing.T) {
	window := 15 * time.Minute
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	provider := &rotatingCredentialsProvider{issued: []aws.Credentials{
		{AccessKeyID: "AKID_FIRST", SecretAccessKey: "SECRET_FIRST", SessionToken: "TOKEN_FIRST"},
		{AccessKeyID: "AKID_ROTATED", SecretAccessKey: "SECRET_ROTATED", SessionToken: "TOKEN_ROTATED"},
	}}

	p := newTestPresigner(window, func() time.Time { return now })
	p.creds = provider

	first, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign before rotation: %v", err)
	}
	// Same clock, same window — only the credentials move.
	second, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg")
	if err != nil {
		t.Fatalf("presign after rotation: %v", err)
	}

	if n := provider.callCount(); n != 2 {
		t.Fatalf(
			"provider.Retrieve called %d times across 2 presigns, want 2 — credentials are being snapshotted, not resolved per call",
			n,
		)
	}

	firstQ := queryOf(t, first)
	secondQ := queryOf(t, second)

	if got := firstQ.Get("X-Amz-Security-Token"); got != "TOKEN_FIRST" {
		t.Errorf("X-Amz-Security-Token before rotation = %q, want %q", got, "TOKEN_FIRST")
	}
	if got := secondQ.Get("X-Amz-Security-Token"); got != "TOKEN_ROTATED" {
		t.Errorf(
			"X-Amz-Security-Token after rotation = %q, want %q — this URL would 403 ExpiredToken once the first token lapsed",
			got, "TOKEN_ROTATED",
		)
	}
	if got := secondQ.Get("X-Amz-Credential"); !strings.HasPrefix(got, "AKID_ROTATED/") {
		t.Errorf("X-Amz-Credential after rotation = %q, want the rotated access key", got)
	}
	if firstQ.Get("X-Amz-Signature") == secondQ.Get("X-Amz-Signature") {
		t.Error("signature unchanged across a credential rotation; the new secret was not used")
	}
}

// The flip side of resolving per call: while the credentials hold steady, the
// window must still yield a byte-identical URL. That cache stability is the
// entire reason windowedPresigner exists instead of the SDK's presign client,
// and it must survive the move to a provider.
func TestPresignGetStableWhileCredentialsHold(t *testing.T) {
	window := 15 * time.Minute
	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	provider := &rotatingCredentialsProvider{issued: []aws.Credentials{
		{AccessKeyID: "AKID", SecretAccessKey: "SECRET", SessionToken: "TOKEN"},
	}}

	now := base
	p := newTestPresigner(window, func() time.Time { return now })
	p.creds = provider

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
		t.Fatalf("URL changed within a window on unchanged credentials:\n  %s\n  %s", u1, u2)
	}
}

// A provider that can't resolve must surface as an error, not as a URL signed
// with zero-value credentials that would 403 at fetch time.
func TestPresignGetSurfacesCredentialFailure(t *testing.T) {
	window := 15 * time.Minute
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	sentinel := errors.New("imds unreachable")

	p := newTestPresigner(window, func() time.Time { return now })
	p.creds = failingCredentialsProvider{err: sentinel}

	if _, err := p.presignGet(context.Background(), "user_id=u1/variant=full/photo1.jpg"); !errors.Is(err, sentinel) {
		t.Fatalf("presignGet error = %v, want it to wrap %v", err, sentinel)
	}
}

// queryOf parses a presigned URL and returns its query parameters.
func queryOf(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse presigned url: %v", err)
	}
	return u.Query()
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
