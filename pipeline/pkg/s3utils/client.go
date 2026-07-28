package s3utils

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newS3Client builds an AWS SDK v2 S3 client.
// When AccessKeyID/SecretAccessKey are set, those static credentials are used;
// otherwise the default credential chain applies.
// When Endpoint is set, path-style addressing is enabled for MinIO.
func newS3Client(ctx context.Context, cfg Config) (*s3.Client, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	loadOpts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if cfg.AccessKeyID != "" {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3utils: load aws config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	}), nil
}
