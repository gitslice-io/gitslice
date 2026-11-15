package r2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"gitslice.io/gitslice/internal/storage"
)

type Config struct {
	Endpoint        string // account-level R2 host, no bucket path, e.g. https://<acct>.r2.cloudflarestorage.com
	Region          string // e.g. "auto"
	Bucket          string
	Prefix          string // optional key prefix, joined with "/" (no leading/trailing slash needed)
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
}

type Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// ConfigFromEnv reads R2_ENDPOINT, R2_REGION, R2_BUCKET, R2_PREFIX,
// R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_USE_PATH_STYLE.
func ConfigFromEnv() Config {
	usePathStyle, _ := strconv.ParseBool(os.Getenv("R2_USE_PATH_STYLE"))
	return Config{
		Endpoint:        os.Getenv("R2_ENDPOINT"),
		Region:          os.Getenv("R2_REGION"),
		Bucket:          os.Getenv("R2_BUCKET"),
		Prefix:          os.Getenv("R2_PREFIX"),
		AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		UsePathStyle:    usePathStyle,
	}
}

func New(cfg Config) (*Store, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.AccessKeyID = strings.TrimSpace(cfg.AccessKeyID)
	cfg.SecretAccessKey = strings.TrimSpace(cfg.SecretAccessKey)

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("r2 endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("r2 bucket is required")
	}
	if cfg.AccessKeyID == "" {
		return nil, fmt.Errorf("r2 access key id is required")
	}
	if cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("r2 secret access key is required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.EndpointResolver = s3.EndpointResolverFromURL(cfg.Endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &Store{
		client: client,
		bucket: cfg.Bucket,
		prefix: cfg.Prefix,
	}, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if r == nil {
		return fmt.Errorf("object reader is required")
	}
	objectKey, err := resolveObjectKey(s.prefix, key)
	if err != nil {
		return err
	}
	// R2 requires an explicit Content-Length on PutObject and rejects chunked
	// uploads with HTTP 411 (MissingContentLength). When the caller provides an
	// io.ReadSeeker (such as a staged *os.File or *bytes.Reader), determine its
	// byte length via Seek and stream it directly without a duplicate heap copy.
	if rs, ok := r.(io.ReadSeeker); ok {
		if size, ok := seekableRemainingSize(rs); ok {
			_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
				Bucket:        aws.String(s.bucket),
				Key:           aws.String(objectKey),
				Body:          rs,
				ContentLength: aws.Int64(size),
			})
			return err
		}
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          bytes.NewReader(buf),
		ContentLength: aws.Int64(int64(len(buf))),
	})
	return err
}

// Copy performs a server-side S3 CopyObject within the bucket so staged objects
// can be promoted without re-downloading and re-uploading their payload.
func (s *Store) Copy(ctx context.Context, dstKey, srcKey string) error {
	dstObjectKey, err := resolveObjectKey(s.prefix, dstKey)
	if err != nil {
		return err
	}
	srcObjectKey, err := resolveObjectKey(s.prefix, srcKey)
	if err != nil {
		return err
	}
	if dstObjectKey == srcObjectKey {
		return nil
	}
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(dstObjectKey),
		CopySource: aws.String(encodeCopySource(s.bucket, srcObjectKey)),
	})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w: %w", storage.ErrNotFound, err)
		}
		return err
	}
	return nil
}

func seekableRemainingSize(rs io.ReadSeeker) (int64, bool) {
	cur, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, false
	}
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false
	}
	if _, err := rs.Seek(cur, io.SeekStart); err != nil {
		return 0, false
	}
	if end < cur {
		return 0, false
	}
	return end - cur, true
}

func encodeCopySource(bucket, key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return url.PathEscape(bucket) + "/" + strings.Join(parts, "/")
}

func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	objectKey, err := resolveObjectKey(s.prefix, key)
	if err != nil {
		return nil, err
	}
	byteRange, err := rangeHeader(offset, length)
	if err != nil {
		return nil, err
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}
	if byteRange != "" {
		input.Range = aws.String(byteRange)
	}
	out, err := s.client.GetObject(ctx, input)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %w", storage.ErrNotFound, err)
		}
		return nil, err
	}
	return out.Body, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	objectKey, err := resolveObjectKey(s.prefix, key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if isNotFound(err) {
		return nil
	}
	return err
}

func resolveObjectKey(prefix, key string) (string, error) {
	cleaned, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return strings.TrimLeft(path.Join(prefix, cleaned), "/"), nil
}

func cleanKey(key string) (string, error) {
	cleaned := filepath.Clean(strings.TrimPrefix(key, "/"))
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("invalid object key %q", cleaned)
	}
	return filepath.ToSlash(cleaned), nil
}

func rangeHeader(offset, length int64) (string, error) {
	if offset < 0 {
		offset = 0
	}
	if offset == 0 && length <= 0 {
		return "", nil
	}
	if length <= 0 {
		return fmt.Sprintf("bytes=%d-", offset), nil
	}
	if offset > math.MaxInt64-length {
		return "", fmt.Errorf("invalid object range offset %d length %d", offset, length)
	}
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1), nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
