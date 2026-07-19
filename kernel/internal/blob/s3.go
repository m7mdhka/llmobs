package blob

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

// S3Store is the scale-profile blob backend: an S3-compatible object store (AWS S3,
// MinIO, GCS/Azure via their S3 gateways — the #101 "abstract the backend" requirement,
// met by using the S3 API surface). It is the analogue of the ClickHouse storage adapter:
// the same blob.Store interface the local filesystem adapter implements, so nothing above
// the seam changes between profiles.
//
// Security parity with the local owner-only files: objects are written PRIVATE (the S3
// default — never a public-read ACL) and encrypted at rest. A blob carries the same
// sensitivity as the data it belongs to, so its at-rest protection must not be weaker than
// the local adapter's.
//
// Encryption-at-rest is enforced by the BUCKET's default-encryption policy (operator-
// configured — SSE-S3 or SSE-KMS), which applies to every write automatically and is the
// only portable choice: forcing a per-object SSE header couples the adapter to the
// backend's KMS setup (a stock MinIO without KMS rejects it outright). The operator MUST
// enable bucket default-encryption and block-public-access — this is documented as a
// deployment requirement. RequestSSE opts INTO an explicit per-object SSE-S3 header for
// backends that require it (a KMS-configured MinIO/KES); it is off by default.
type S3Store struct {
	client *minio.Client
	bucket string
	sse    encrypt.ServerSide // nil unless RequestSSE was set
}

var _ Store = (*S3Store)(nil)

// S3Config configures an S3Store. Endpoint is host[:port] (no scheme); UseSSL selects
// https. Region may be empty for MinIO.
type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	// RequestSSE sends an explicit per-object SSE-S3 header. Leave false to rely on the
	// bucket's default-encryption policy (the portable default); set true only when the
	// backend requires the header AND has KMS/KES configured.
	RequestSSE bool
}

// NewS3Store builds an S3-backed blob store and verifies the bucket exists.
func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("blob s3: endpoint and bucket are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("blob s3 client: %w", err)
	}
	ok, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blob s3 bucket check: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("blob s3: bucket %q does not exist", cfg.Bucket)
	}
	s := &S3Store{client: client, bucket: cfg.Bucket}
	if cfg.RequestSSE {
		s.sse = encrypt.NewSSE()
	}
	return s, nil
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType:          contentType,
		ServerSideEncryption: s.sse, // nil => bucket default-encryption applies; object ACL stays private
	})
	if err != nil {
		return fmt.Errorf("blob s3 put: %w", err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("blob s3 get: %w", err)
	}
	// GetObject is lazy; Stat is where a missing key surfaces. Map it to ErrNotFound and
	// close the (empty) object handle.
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("blob s3 stat: %w", err)
	}
	return obj, ObjectInfo{
		Key:         key,
		Size:        stat.Size,
		ContentType: stat.ContentType,
		ModifiedAt:  stat.LastModified,
	}, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	// S3 delete is idempotent (removing a missing key is not an error), matching the
	// local adapter's Delete contract.
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("blob s3 delete: %w", err)
	}
	return nil
}
