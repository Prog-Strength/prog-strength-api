package activity

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// photoOrphanTagKey / photoOrphanTagValue mark a superseded (or never-finalized)
// photo object for lifecycle reaping. They MUST stay identical to the lifecycle
// rule's tag filter in prog-strength-infra modules/activity_photo_storage — the
// rule expires only objects carrying this exact tag, so a live photo (untagged)
// is never reaped.
const (
	photoOrphanTagKey   = "photo-status"
	photoOrphanTagValue = "orphaned"
)

// photoCacheControl is written on every Put. It is safe to mark photo objects
// immutable with a one-year max-age because keys are photo-id-addressed and
// never overwritten: a re-uploaded or edited photo is a brand-new key, so a
// cached URL can never serve stale bytes.
const photoCacheControl = "private, max-age=31536000, immutable"

// unsignedPayload is the payload hash used for S3 GET presigning: the body is
// not part of the request the browser will make, so SigV4 uses this sentinel
// rather than hashing an (absent) body.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// s3Service is the SigV4 service name for Amazon S3.
const s3Service = "s3"

// PhotoStore is the object-storage seam for activity photos. The S3
// implementation is used in prod; FakePhotoStore keeps handler tests hermetic.
type PhotoStore interface {
	// Put writes body under key with the given contentType.
	Put(ctx context.Context, key, contentType string, body []byte) error
	// PresignGet returns a windowed, cache-stable GET URL for key.
	PresignGet(ctx context.Context, key string) (string, error)
	// TagOrphaned best-effort marks a superseded object for lifecycle reaping.
	TagOrphaned(ctx context.Context, key string) error
}

// windowedPresigner mints SigV4 presigned S3 GET URLs that are byte-identical
// for every call inside the same time window. The standard S3 presign client
// bakes time.Now() into the signature, so it produces a different URL on every
// call — defeating browser caching of the photo. This presigner instead signs
// with an explicit signingTime snapped to the current window boundary
// (now.Truncate(window)) and an X-Amz-Expires of 2*window, so two requests
// within the same window yield the exact same URL and the browser treats the
// photo as a single cacheable resource. Doubling the expiry means a URL minted
// near the end of a window is still valid for at least a full window afterward.
type windowedPresigner struct {
	creds  aws.Credentials
	signer *v4.Signer
	region string
	bucket string
	window time.Duration
	now    func() time.Time
}

// presignGet builds a GET request for the virtual-hosted-style S3 URL of key,
// sets X-Amz-Expires to 2*window (in whole seconds), and signs it with SigV4
// query presigning using a signing time snapped to the current window. It
// returns the signed URL.
func (p *windowedPresigner) presignGet(ctx context.Context, key string) (string, error) {
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	signingTime := now().Truncate(p.window)

	endpoint := fmt.Sprintf(
		"https://%s.s3.%s.amazonaws.com/%s",
		p.bucket, p.region, escapeKeyPath(key),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	// X-Amz-Expires must be in the query BEFORE signing: SigV4 query presigning
	// includes it in the signed canonical query string.
	expires := int64((2 * p.window) / time.Second)
	q := req.URL.Query()
	q.Set("X-Amz-Expires", strconv.FormatInt(expires, 10))
	req.URL.RawQuery = q.Encode()

	signedURL, _, err := p.signer.PresignHTTP(
		ctx, p.creds, req, unsignedPayload, s3Service, p.region, signingTime,
	)
	if err != nil {
		return "", err
	}
	return signedURL, nil
}

// escapeKeyPath escapes each path segment of an S3 key while preserving the
// "/" separators that make up the Hive-partitioned layout. net/url's
// PathEscape escapes "/", so we split on "/" and escape each segment.
func escapeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// S3PhotoStore stores activity photos in an S3 bucket via aws-sdk-go-v2 and
// hands out windowed presigned GET URLs.
type S3PhotoStore struct {
	client  *s3.Client
	presign *windowedPresigner
	bucket  string
}

// Compile-time check that *S3PhotoStore satisfies PhotoStore.
var _ PhotoStore = (*S3PhotoStore)(nil)

// NewS3PhotoStore builds an S3-backed photo store for the given bucket. window
// controls the cache-stability window of presigned GET URLs. Credentials come
// from the AWS default chain (the EC2 instance role in prod) and are resolved
// once here so the presigner can sign without a per-call provider lookup.
func NewS3PhotoStore(ctx context.Context, bucket, region string, window time.Duration) (*S3PhotoStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)

	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}

	return &S3PhotoStore{
		client: client,
		presign: &windowedPresigner{
			creds:  creds,
			signer: v4.NewSigner(),
			region: region,
			bucket: bucket,
			window: window,
			now:    time.Now,
		},
		bucket: bucket,
	}, nil
}

func (s *S3PhotoStore) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(body),
		ContentType:  aws.String(contentType),
		CacheControl: aws.String(photoCacheControl),
	})
	return err
}

func (s *S3PhotoStore) PresignGet(ctx context.Context, key string) (string, error) {
	return s.presign.presignGet(ctx, key)
}

func (s *S3PhotoStore) TagOrphaned(ctx context.Context, key string) error {
	_, err := s.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String(photoOrphanTagKey), Value: aws.String(photoOrphanTagValue)},
			},
		},
	})
	return err
}
