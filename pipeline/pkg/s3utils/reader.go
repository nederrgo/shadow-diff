package s3utils

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectGetter lists and downloads objects (tests inject a fake).
type ObjectGetter interface {
	ListKeys(ctx context.Context, prefix string) ([]string, error)
	GetObject(ctx context.Context, key string) ([]byte, error)
}

// S3Reader downloads JSONL session artifacts from S3/MinIO.
type S3Reader struct {
	cfg    Config
	client ObjectGetter
}

// NewS3Reader builds a reader with a real S3 client (path-style when Endpoint set).
func NewS3Reader(ctx context.Context, cfg Config) (*S3Reader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c, err := newS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &S3Reader{cfg: cfg, client: &s3Getter{client: c, bucket: cfg.Bucket}}, nil
}

// NewS3ReaderWithGetter is for tests.
func NewS3ReaderWithGetter(cfg Config, g ObjectGetter) (*S3Reader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if g == nil {
		return nil, fmt.Errorf("s3utils: ObjectGetter is required")
	}
	return &S3Reader{cfg: cfg, client: g}, nil
}

// ListJSONLKeys returns object keys under the session data-type prefix ending in .jsonl.
func (r *S3Reader) ListJSONLKeys(ctx context.Context) ([]string, error) {
	keys, err := r.client.ListKeys(ctx, r.cfg.ObjectKeyPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasSuffix(k, "/") {
			continue
		}
		if !strings.HasSuffix(k, ".jsonl") {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ReadAllJSONL downloads every .jsonl under the prefix and returns non-empty lines.
func (r *S3Reader) ReadAllJSONL(ctx context.Context) ([][]byte, error) {
	keys, err := r.ListJSONLKeys(ctx)
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	for _, key := range keys {
		body, err := r.client.GetObject(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("s3utils: get %s: %w", key, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines = append(lines, []byte(line))
		}
	}
	return lines, nil
}

type s3Getter struct {
	client *s3.Client
	bucket string
}

func (g *s3Getter) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	var (
		keys  []string
		token *string
	)
	for {
		out, err := g.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(g.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3utils: list %q: %w", prefix, err)
		}
		for _, obj := range out.Contents {
			if obj.Key == nil {
				continue
			}
			keys = append(keys, *obj.Key)
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	return keys, nil
}

func (g *s3Getter) GetObject(ctx context.Context, key string) ([]byte, error) {
	out, err := g.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(g.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}
