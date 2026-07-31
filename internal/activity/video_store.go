package activity

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// videoOrphanTagKey / videoOrphanTagValue mark an abandoned or rejected video
// object for lifecycle reaping. They MUST stay identical to the lifecycle rule's
// tag filter in prog-strength-infra modules/activity_video_storage — the rule
// expires only objects carrying this exact tag, so a live video (untagged) is
// never reaped. Deliberately distinct from the photo bucket's "photo-status" so
// the two buckets' rules can diverge without cross-talk.
const (
	videoOrphanTagKey   = "video-status"
	videoOrphanTagValue = "orphaned"
)

// videoCacheControl mirrors photoCacheControl: keys are video-id-addressed and
// never overwritten, so a cached URL can never serve stale bytes.
const videoCacheControl = "private, max-age=31536000, immutable"

// ErrObjectNotFound is returned by VideoStore.Head when no object exists at the
// key. The commit path treats it as "the client never finished uploading",
// which is a 409 rather than a 500.
var ErrObjectNotFound = errors.New("activity: object not found")

// VideoStore is the object-storage seam for activity videos. It differs from
// PhotoStore in the one way that matters: it can hand out a presigned PUT so
// the CLIENT uploads directly to S3. Video bytes never pass through this
// process — the photo path's io.ReadAll + Put([]byte) shape cannot carry a
// multi-hundred-megabyte file on a host that also runs MCP, the agent, Caddy,
// Prometheus and Grafana.
//
// Put remains, but only for the poster JPEG, which is small and already flows
// through the server-authoritative image pipeline.
type VideoStore interface {
	// PresignPut returns a short-lived URL the client PUTs the video to.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
	// PresignGet returns a windowed, cache-stable GET URL for key.
	PresignGet(ctx context.Context, key string) (string, error)
	// Head returns the stored object's size in bytes, or ErrObjectNotFound.
	// This is how the commit path learns a video's REAL size — the client's
	// claimed size is never trusted.
	Head(ctx context.Context, key string) (int64, error)
	// Put writes body under key. Poster JPEGs only.
	Put(ctx context.Context, key, contentType string, body []byte) error
	// TagOrphaned best-effort marks an abandoned/rejected object for reaping.
	TagOrphaned(ctx context.Context, key string) error
}

// S3VideoStore stores activity videos in an S3 bucket via aws-sdk-go-v2.
type S3VideoStore struct {
	client   *s3.Client
	presign  *windowedPresigner
	uploader *s3.PresignClient
	bucket   string
}

// Compile-time check that *S3VideoStore satisfies VideoStore.
var _ VideoStore = (*S3VideoStore)(nil)

// NewS3VideoStore builds an S3-backed video store. window controls the
// cache-stability window of presigned GET URLs, exactly as for photos.
//
// Two distinct signers are held on purpose. GET reuses windowedPresigner so
// playback URLs stay byte-identical within a window and the browser can cache
// them. PUT uses the SDK's standard presign client, because an upload URL is
// single-use and short-lived — cache stability is meaningless and a
// window-snapped signing time would only shorten its usable life.
func NewS3VideoStore(ctx context.Context, bucket, region string, window time.Duration) (*S3VideoStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)

	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}

	return &S3VideoStore{
		client: client,
		presign: &windowedPresigner{
			creds:  creds,
			signer: v4.NewSigner(),
			region: region,
			bucket: bucket,
			window: window,
			now:    time.Now,
			// Applied at READ time because the PUT can't carry it: see
			// PresignPut. Same immutability rationale as photos — keys are
			// video-id-addressed and never overwritten.
			responseCacheControl: videoCacheControl,
		},
		uploader: s3.NewPresignClient(client),
		bucket:   bucket,
	}, nil
}

// PresignPut mints an upload URL valid for ttl.
//
// NOTE ON SIZE ENFORCEMENT: a presigned PUT cannot constrain the object's size —
// only a presigned POST policy's content-length-range can, and that would force
// the browser onto a multipart form and cost the simple XHR upload-progress path.
// The size cap is therefore enforced at COMMIT: uploadComplete HEADs the object,
// and anything over videos.max_upload_bytes is tagged orphaned and rejected
// before its row is ever marked ready. The exposure is a transient oversize
// object in a private single-user bucket, reaped by lifecycle. This tradeoff is
// sanctioned explicitly by sows/activity-videos.md § Write Path; do not remove
// the HEAD check without replacing it with a POST policy.
func (s *S3VideoStore) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	// NOTHING may be set here that the browser will not send as a header.
	// Every field the SDK turns into a signed header becomes one the client
	// MUST reproduce byte-for-byte, or S3 answers 403 SignatureDoesNotMatch.
	// VideoStrip's XHR sends exactly one header: Content-Type.
	//
	// CacheControl used to be set here and put `cache-control` into
	// X-Amz-SignedHeaders, so every upload 403'd after appearing to transfer.
	// Caching is instead applied at READ time via the presigned GET's
	// response-cache-control override, which needs no client cooperation.
	// TestPresignPut_SignsOnlyHeadersTheBrowserSends guards this.
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

func (s *S3VideoStore) PresignGet(ctx context.Context, key string) (string, error) {
	return s.presign.presignGet(ctx, key)
}

func (s *S3VideoStore) Head(ctx context.Context, key string) (int64, error) {
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

func (s *S3VideoStore) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(body),
		ContentType:  aws.String(contentType),
		CacheControl: aws.String(videoCacheControl),
	})
	return err
}

func (s *S3VideoStore) TagOrphaned(ctx context.Context, key string) error {
	_, err := s.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String(videoOrphanTagKey), Value: aws.String(videoOrphanTagValue)},
			},
		},
	})
	return err
}
