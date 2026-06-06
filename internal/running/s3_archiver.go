package running

import (
	"bytes"
	"context"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// tcxContentType is the MIME type stored on archived TCX objects. Garmin's
// vendor type is the most precise; it's plain XML on the wire.
const tcxContentType = "application/vnd.garmin.tcx+xml"

// Archiver stores and removes the raw TCX file backing a session. The
// repository writes through it so the "DB row + S3 object" pair stays
// consistent; the in-memory implementation (MemoryArchiver) is used in
// dev/tests when no bucket is configured.
type Archiver interface {
	Put(ctx context.Context, key string, body []byte) error
	Delete(ctx context.Context, key string) error
}

// S3Archiver archives TCX files to an S3 bucket via aws-sdk-go-v2.
type S3Archiver struct {
	client *s3.Client
	bucket string
}

// Compile-time check that *S3Archiver satisfies Archiver.
var _ Archiver = (*S3Archiver)(nil)

// NewS3Archiver builds an S3-backed archiver for the given bucket.
// Credentials come from the AWS default chain — on the EC2 host that's
// the instance role, the same source Litestream uses for backups, so
// there's nothing extra to wire up.
func NewS3Archiver(ctx context.Context, bucket string) (*S3Archiver, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &S3Archiver{client: s3.NewFromConfig(cfg), bucket: bucket}, nil
}

func (a *S3Archiver) Put(ctx context.Context, key string, body []byte) error {
	ct := tcxContentType
	_, err := a.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &a.bucket,
		Key:         &key,
		Body:        bytes.NewReader(body),
		ContentType: &ct,
	})
	return err
}

func (a *S3Archiver) Delete(ctx context.Context, key string) error {
	_, err := a.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &a.bucket,
		Key:    &key,
	})
	return err
}
