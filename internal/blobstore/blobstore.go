// Package blobstore is a thin wrapper over an S3-compatible object store
// (minio-go) for call recordings. A nil *Store is a valid "disabled" value:
// callers check Enabled() and fall back to local-disk storage/serving, so the
// whole S3 path is opt-in per tenant (Resolver) without conditionals leaking everywhere.
package blobstore

import (
	"context"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config is one tenant's S3 connection config (tenants.s3_*, see Resolver).
type Config struct {
	Bucket    string
	Region    string
	Endpoint  string // empty = AWS (s3.amazonaws.com); set for MinIO/R2/etc.
	AccessKey string
	SecretKey string
	Prefix    string // object-key prefix; default "recordings"
	UseSSL    bool
	// Transport carries S3 requests; nil = minio's default. Set to the
	// safehttp-guarded transport, since the endpoint is tenant-supplied. IAM
	// role lookups use their own client (they must reach the metadata address).
	Transport http.RoundTripper
}

// Store is an S3 bucket handle scoped to a key prefix.
type Store struct {
	client *minio.Client
	bucket string
	prefix string
}

// New builds a Store, or returns (nil, nil) when S3 isn't configured (no
// bucket) so callers can treat the nil Store as "S3 disabled".
func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, nil
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	// Static key/secret if given; otherwise fall back to the AWS IAM role
	// (EC2 instance profile / ECS task role / EKS IRSA via the metadata
	// endpoint) so an AWS deployment can authenticate with no secrets in config.
	var creds *credentials.Credentials
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = credentials.NewIAM("")
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:     creds,
		Secure:    cfg.UseSSL,
		Region:    cfg.Region,
		Transport: cfg.Transport,
	})
	if err != nil {
		return nil, err
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "recordings"
	}
	return &Store{client: cli, bucket: cfg.Bucket, prefix: prefix}, nil
}

// Enabled reports whether S3 is configured. Safe to call on a nil receiver.
func (s *Store) Enabled() bool { return s != nil && s.client != nil }

// Key builds the date-wise object key: <prefix>/<tenant>/YYYY/MM/DD/<name>.
func (s *Store) Key(tenant string, t time.Time, name string) string {
	return path.Join(s.prefix, tenant, t.Format("2006/01/02"), name)
}

// PutFile uploads a local file to key.
func (s *Store) PutFile(ctx context.Context, key, filePath, contentType string) error {
	_, err := s.client.FPutObject(ctx, s.bucket, key, filePath, minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Delete removes the object at key.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// Probe checks the store can take a tenant's recordings: it writes and then
// deletes <prefix>/<tenant>/.tring-write-test, so a wrong bucket, region or
// permission fails when the config is saved, not after the next call.
func (s *Store) Probe(ctx context.Context, tenant string) error {
	key := path.Join(s.prefix, tenant, ".tring-write-test")
	if _, err := s.client.PutObject(ctx, s.bucket, key, strings.NewReader("ok"), 2,
		minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return err
	}
	return s.Delete(ctx, key)
}

// Get opens an object for reading; the caller closes it. Returns an error if
// the object is missing (Stat is checked up front so a missing key surfaces
// before any bytes are streamed, letting the caller send a clean 404).
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, err
	}
	return obj, nil
}

// IsNotFound reports whether an error from Get means the object doesn't exist,
// as opposed to the store being unreachable or refusing the request.
func IsNotFound(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

// Describe turns a store error into a short message safe to show the tenant
// admin who entered the config: the S3 error code and message, or a generic
// "unreachable" (dial errors would name internal addresses).
func Describe(err error) string {
	if r := minio.ToErrorResponse(err); r.Code != "" {
		return r.Code + ": " + r.Message
	}
	return "could not reach the storage endpoint"
}
