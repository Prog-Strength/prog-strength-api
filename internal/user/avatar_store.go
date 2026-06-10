package user

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// presignExpiry bounds the lifetime of a GET URL handed to the client. Long
// enough for an open tab to keep rendering the avatar across navigation, short
// enough that a leaked URL isn't a durable hotlink (per the SOW open question).
const presignExpiry = 1 * time.Hour

// orphanTagKey / orphanTagValue mark a superseded avatar object for lifecycle
// reaping. They MUST stay identical to the lifecycle rule's tag filter in the
// prog-strength-infra avatar_storage module — the rule expires only objects
// carrying this exact tag, so the current avatar (untagged) is never reaped.
const (
	orphanTagKey   = "avatar-status"
	orphanTagValue = "orphaned"
)

// contentTypeToExt maps the allowlisted upload content types to the file
// extension used in the S3 key. The handler sniffs the content type itself
// (don't trust the client) and looks the extension up here.
var contentTypeToExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// ExtForContentType returns the avatar file extension for a sniffed content
// type and whether it's in the allowlist.
func ExtForContentType(contentType string) (string, bool) {
	ext, ok := contentTypeToExt[contentType]
	return ext, ok
}

// AvatarKey builds the S3 object key for a user's avatar. Each upload gets a
// fresh random component so a changed avatar is a new key — the old presigned
// URL just expires naturally (no cache-busting headaches) and "latest wins" is
// trivially correct via the avatar_key column. Partitioned per user, no date
// partitioning: there is exactly one current avatar per user.
func AvatarKey(userID, ext string) string {
	return fmt.Sprintf("user_id=%s/%s.%s", userID, id.New(), ext)
}

// AvatarStore is the object-storage seam for avatars. The S3 implementation is
// used in prod; a fake (FakeAvatarStore) keeps handler tests hermetic.
type AvatarStore interface {
	// Put writes body under key with the given contentType.
	Put(ctx context.Context, key, contentType string, body []byte) error
	// PresignGet returns a time-limited GET URL for key.
	PresignGet(ctx context.Context, key string) (string, error)
	// TagOrphaned best-effort marks a superseded object for lifecycle reaping.
	TagOrphaned(ctx context.Context, key string) error
}

// S3AvatarStore stores avatars in an S3 bucket via aws-sdk-go-v2.
type S3AvatarStore struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// Compile-time check that *S3AvatarStore satisfies AvatarStore.
var _ AvatarStore = (*S3AvatarStore)(nil)

// NewS3AvatarStore builds an S3-backed avatar store for the given bucket.
// Credentials come from the AWS default chain (the EC2 instance role in prod),
// the same source the TCX archiver uses.
func NewS3AvatarStore(ctx context.Context, bucket string) (*S3AvatarStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)
	return &S3AvatarStore{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  bucket,
	}, nil
}

func (s *S3AvatarStore) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	return err
}

func (s *S3AvatarStore) PresignGet(ctx context.Context, key string) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(presignExpiry))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *S3AvatarStore) TagOrphaned(ctx context.Context, key string) error {
	_, err := s.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String(orphanTagKey), Value: aws.String(orphanTagValue)},
			},
		},
	})
	return err
}
