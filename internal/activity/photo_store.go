package activity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	// --- two-phase upload -------------------------------------------------

	// PresignPut returns a short-lived URL the client PUTs the original to.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
	// Head returns the stored object's size in bytes, or ErrObjectNotFound.
	// This is how commit learns the upload's REAL size — the client's claimed
	// size is never trusted.
	Head(ctx context.Context, key string) (int64, error)
	// Get reads an object back. The worker uses it to fetch the staged
	// original it is about to strip.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes an object outright. Used for the staged original, which
	// is the only copy carrying the source's GPS and must stop existing as
	// soon as it is redundant — not in a day, when the lifecycle rule runs.
	Delete(ctx context.Context, key string) error
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
//
// Cache stability holds only while the credentials do. In prod they are the EC2
// instance role's temporary credentials, which rotate every few hours, and a
// rotation changes X-Amz-Credential, X-Amz-Security-Token and the signature —
// so the URL changes mid-window and the browser refetches once. That is the
// correct trade: the alternative, pinning credentials to keep the URL stable,
// is what made every photo 403 ExpiredToken once the pinned token lapsed.
type windowedPresigner struct {
	// creds is a PROVIDER, not a credential value, and is resolved on every
	// presign. Snapshotting the value here means the presigner keeps signing
	// with a session token that the SDK has long since rotated away from, and
	// S3 rejects those URLs with 403 ExpiredToken. In prod this is the SDK's
	// credential cache, so per-call resolution is a memory read, not an IMDS
	// round trip.
	creds  aws.CredentialsProvider
	signer *v4.Signer
	region string
	bucket string
	window time.Duration
	now    func() time.Time
	// responseCacheControl, when non-empty, adds S3's response-cache-control
	// override to the signed query so the GET response carries that header.
	//
	// Photos leave this empty: the server PUTs photo objects itself and sets
	// Cache-Control at write time. Videos CAN'T — the browser does the PUT, and
	// signing a cache-control header the browser doesn't send makes every
	// upload 403. Applying it as a signed query param at read time achieves the
	// same caching with no client cooperation at all.
	responseCacheControl string
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

	// Resolved per call, never cached on the struct: see the type comment.
	creds, err := p.creds.Retrieve(ctx)
	if err != nil {
		return "", err
	}

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
	if p.responseCacheControl != "" {
		q.Set("response-cache-control", p.responseCacheControl)
	}
	req.URL.RawQuery = q.Encode()

	signedURL, _, err := p.signer.PresignHTTP(
		ctx, creds, req, unsignedPayload, s3Service, p.region, signingTime,
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
	client *s3.Client
	// presign mints windowed, cache-stable GET URLs for reads.
	presign *windowedPresigner
	// uploader mints one-shot presigned PUTs for the client's direct upload.
	// Separate from presign because an upload URL wants a short, exact TTL,
	// not a cache-stable window.
	uploader *s3.PresignClient
	bucket   string
}

// Compile-time check that *S3PhotoStore satisfies PhotoStore.
var _ PhotoStore = (*S3PhotoStore)(nil)

// NewS3PhotoStore builds an S3-backed photo store for the given bucket. window
// controls the cache-stability window of presigned GET URLs. Credentials come
// from the AWS default chain (the EC2 instance role in prod); the PROVIDER is
// handed to the presigner, not a resolved snapshot of it, so rotation of the
// instance role's temporary credentials is picked up on the next presign.
func NewS3PhotoStore(ctx context.Context, bucket, region string, window time.Duration) (*S3PhotoStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)

	return &S3PhotoStore{
		client:   client,
		uploader: s3.NewPresignClient(client),
		presign: &windowedPresigner{
			creds:  cfg.Credentials,
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

// PresignPut returns a short-lived URL the browser PUTs the original photo to,
// bypassing this process entirely.
//
// NOTHING may be set here that the browser will not send as a header. Every
// field the SDK turns into a signed header becomes one the client MUST
// reproduce byte-for-byte, or S3 answers 403 SignatureDoesNotMatch. The
// uploader sends exactly one: Content-Type.
//
// This is not hypothetical. The video path set CacheControl here, which put
// `cache-control` into X-Amz-SignedHeaders and made every upload 403 *after
// appearing to transfer successfully* — see the same note on
// S3VideoStore.PresignPut. Read-time caching is applied through the presigned
// GET's response-cache-control override instead, which needs no client
// cooperation.
func (s *S3PhotoStore) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	req, err := s.uploader.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// Head returns the stored object's size, or ErrObjectNotFound when the client
// never completed its upload.
func (s *S3PhotoStore) Head(ctx context.Context, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return 0, ErrObjectNotFound
		}
		return 0, err
	}
	if out.ContentLength == nil {
		return 0, nil
	}
	return *out.ContentLength, nil
}

// Get reads an object into memory. Only the worker calls this, for the staged
// original it is about to strip — which is why buffering a whole photo here is
// acceptable: no request deadline, and a concurrency of one.
func (s *S3PhotoStore) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Delete removes an object outright rather than tagging it for the lifecycle
// rule. Reserved for the staged original: it is the only copy carrying the
// source's EXIF/GPS, so once the stripped copy is written it should stop
// existing immediately, not at the next lifecycle sweep.
func (s *S3PhotoStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}
