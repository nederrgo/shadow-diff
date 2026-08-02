package s3utils

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const deleteBatchSize = 1000

// ObjectBulkDeleter lists and deletes keys under a prefix (tests inject a fake).
type ObjectBulkDeleter interface {
	ListKeys(ctx context.Context, prefix string) ([]string, error)
	DeleteKeys(ctx context.Context, keys []string) error
}

// DeletePrefix recursively deletes all objects under prefix in cfg.Bucket.
// It never deletes the bucket — only keys matching the prefix (BYOB-safe).
func DeletePrefix(ctx context.Context, cfg Config, prefix string) error {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return fmt.Errorf("s3utils: bucket is required for DeletePrefix")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("s3utils: prefix is required for DeletePrefix")
	}
	c, err := newS3Client(ctx, cfg)
	if err != nil {
		return err
	}
	return deletePrefixWith(ctx, prefix, &s3BulkDeleter{client: c, bucket: cfg.Bucket})
}

// DeletePrefixWith is for tests.
func DeletePrefixWith(ctx context.Context, prefix string, d ObjectBulkDeleter) error {
	if d == nil {
		return fmt.Errorf("s3utils: ObjectBulkDeleter is required")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("s3utils: prefix is required for DeletePrefix")
	}
	return deletePrefixWith(ctx, prefix, d)
}

func deletePrefixWith(ctx context.Context, prefix string, d ObjectBulkDeleter) error {
	keys, err := d.ListKeys(ctx, prefix)
	if err != nil {
		return err
	}
	var batch []string
	for _, k := range keys {
		if strings.HasSuffix(k, "/") && k == prefix {
			continue
		}
		batch = append(batch, k)
		if len(batch) >= deleteBatchSize {
			if err := d.DeleteKeys(ctx, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		return d.DeleteKeys(ctx, batch)
	}
	return nil
}

type s3BulkDeleter struct {
	client *s3.Client
	bucket string
}

func (d *s3BulkDeleter) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(d.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3utils: list %s: %w", prefix, err)
		}
		for _, obj := range out.Contents {
			if obj.Key != nil && *obj.Key != "" {
				keys = append(keys, *obj.Key)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	return keys, nil
}

func (d *s3BulkDeleter) DeleteKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	objs := make([]types.ObjectIdentifier, 0, len(keys))
	for _, k := range keys {
		objs = append(objs, types.ObjectIdentifier{Key: aws.String(k)})
	}
	out, err := d.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(d.bucket),
		Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("s3utils: delete objects: %w", err)
	}
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		return fmt.Errorf("s3utils: delete %s: %s", aws.ToString(e.Key), aws.ToString(e.Message))
	}
	return nil
}
