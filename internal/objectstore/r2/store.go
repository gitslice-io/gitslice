package r2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	// R2 requires a Content-Length on PutObject and rejects chunked/streaming
	// uploads with HTTP 411 (MissingContentLength). A bare io.Reader makes the
	// SDK stream without a known length, so buffer the body and pass an explicit
	// length via a seekable *bytes.Reader. Objects here are content-addressed
	// trees/blobs, consistent with the buffering the filesystem store does.
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
