package activity

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newTestVideoPresigner builds a real S3VideoStore whose presign client uses
// static credentials, so presigning is exercised for real without AWS access.
func newTestVideoPresigner(t *testing.T) *S3VideoStore {
	t.Helper()
	client := s3.New(s3.Options{
		Region:      "us-east-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "secret", ""),
	})
	return &S3VideoStore{
		client:   client,
		uploader: s3.NewPresignClient(client),
		bucket:   "prog-strength-activity-videos",
	}
}

// signedHeaders pulls the X-Amz-SignedHeaders set out of a presigned URL.
func signedHeaders(t *testing.T, raw string) map[string]bool {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse presigned url: %v", err)
	}
	got := map[string]bool{}
	for _, h := range strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";") {
		if h != "" {
			got[h] = true
		}
	}
	return got
}

// THE 403 REGRESSION.
//
// Every header included in a presigned PUT's X-Amz-SignedHeaders becomes a
// header the client MUST send with byte-identical value, or S3 rejects the
// upload with 403 SignatureDoesNotMatch. The browser sends Content-Type (the
// strip sets it explicitly) and nothing else — so signing anything beyond
// host + content-type makes every upload fail.
//
// This shipped signing `cache-control` too, which no browser sends, and every
// upload 403'd after appearing to transfer. Guard the signed set exactly.
func TestPresignPut_SignsOnlyHeadersTheBrowserSends(t *testing.T) {
	s := newTestVideoPresigner(t)

	raw, err := s.PresignPut(context.Background(), "user_id=u/activity_type=hiking/x.mp4", "video/mp4", 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	got := signedHeaders(t, raw)

	if !got["host"] {
		t.Errorf("host must always be signed, got %v", got)
	}

	// The invariant: the signed set may contain NOTHING the browser doesn't
	// send. VideoStrip's XHR sends exactly one header, Content-Type. Whether
	// the SDK chooses to sign content-type is its business; anything else
	// appearing here is a guaranteed 403.
	browserSends := map[string]bool{"host": true, "content-type": true}
	for h := range got {
		if !browserSends[h] {
			t.Errorf(
				"%q is in X-Amz-SignedHeaders but the browser never sends it, so every upload 403s with SignatureDoesNotMatch (signed set: %v)",
				h, got,
			)
		}
	}
}

// The presigned PUT must carry an expiry and target the right bucket/key.
func TestPresignPut_URLShape(t *testing.T) {
	s := newTestVideoPresigner(t)
	key := "user_id=u/activity_type=hiking/year=2026/month=07/day=31/activity_id=a/variant=video/v.mp4"

	raw, err := s.PresignPut(context.Background(), key, "video/quicktime", 45*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(u.Host, "prog-strength-activity-videos") {
		t.Errorf("host = %q, want the video bucket", u.Host)
	}
	if !strings.Contains(u.Path, "variant=video") {
		t.Errorf("path = %q, want the Hive-partitioned key", u.Path)
	}
	if exp := u.Query().Get("X-Amz-Expires"); exp != "2700" {
		t.Errorf("X-Amz-Expires = %q, want 2700 (45m)", exp)
	}
}

// Caching moved from the PUT to the GET when signing cache-control on the PUT
// turned out to 403 every upload. Assert it actually made the trip: the
// override rides the signed query, so the browser gets a cacheable response
// without having to send anything.
func TestPresignGet_CarriesCacheControlOverride(t *testing.T) {
	s := newTestVideoPresigner(t)
	s.presign = &windowedPresigner{
		creds:                aws.Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"},
		signer:               v4.NewSigner(),
		region:               "us-east-2",
		bucket:               "prog-strength-activity-videos",
		window:               6 * time.Hour,
		now:                  func() time.Time { return time.Unix(1785000000, 0).UTC() },
		responseCacheControl: videoCacheControl,
	}

	raw, err := s.PresignGet(context.Background(), "user_id=u/variant=video/v.mp4")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := u.Query().Get("response-cache-control"); got != videoCacheControl {
		t.Errorf("response-cache-control = %q, want %q", got, videoCacheControl)
	}
	// It must be part of the SIGNED query, or S3 rejects the request.
	if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "host") {
		t.Errorf("expected a signed presigned GET, got %q", raw)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("presigned GET carries no signature: %q", raw)
	}
}

// Photos must be UNAFFECTED: the server PUTs those itself with Cache-Control
// at write time, so their presigner leaves the override empty.
func TestPresignGet_PhotosHaveNoCacheControlOverride(t *testing.T) {
	p := &windowedPresigner{
		creds:  aws.Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"},
		signer: v4.NewSigner(),
		region: "us-east-2",
		bucket: "prog-strength-activity-photos",
		window: 6 * time.Hour,
		now:    func() time.Time { return time.Unix(1785000000, 0).UTC() },
	}
	raw, err := p.presignGet(context.Background(), "user_id=u/variant=full/p.jpg")
	if err != nil {
		t.Fatalf("presignGet: %v", err)
	}
	u, _ := url.Parse(raw)
	if got := u.Query().Get("response-cache-control"); got != "" {
		t.Errorf("photo presign gained response-cache-control = %q, want none", got)
	}
}
