package s3utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

const (
	// DataTypeIngress is the S3 prefix segment for recorded ingress traffic.
	DataTypeIngress = "ingress"
	// DataTypeEgress is the S3 prefix segment for recorded egress traffic.
	DataTypeEgress = "egress"

	ModeRecord = "record"
	ModeReplay = "replay"

	defaultFlushInterval = 5 * time.Second
	defaultFlushSize     = 100
)

// ObjectPutter uploads opaque objects (tests inject a fake).
type ObjectPutter interface {
	PutObject(ctx context.Context, key string, body []byte, contentType string) error
}

// Config holds BYOB S3 settings and the session key path components.
type Config struct {
	Bucket    string
	Endpoint  string
	Region    string
	Namespace string
	TestName  string
	SessionID string
	DataType  string // ingress | egress

	// Optional static credentials (Monarch finalizer / non-env callers).
	AccessKeyID     string
	SecretAccessKey string

	FlushInterval time.Duration
	FlushSize     int
}

// ConfigFromEnv reads S3_* / TEST_* / SESSION_ID and sets DataType.
func ConfigFromEnv(dataType string) (Config, error) {
	cfg := Config{
		Bucket:    strings.TrimSpace(os.Getenv("S3_BUCKET")),
		Endpoint:  strings.TrimSpace(os.Getenv("S3_ENDPOINT")),
		Region:    strings.TrimSpace(os.Getenv("S3_REGION")),
		Namespace: strings.TrimSpace(os.Getenv("TEST_NAMESPACE")),
		TestName:  strings.TrimSpace(os.Getenv("TEST_NAME")),
		SessionID: strings.TrimSpace(os.Getenv("SESSION_ID")),
		DataType:  strings.TrimSpace(dataType),
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks required fields for a record-mode uploader.
func (c Config) Validate() error {
	switch c.DataType {
	case DataTypeIngress, DataTypeEgress:
	default:
		return fmt.Errorf("s3utils: dataType must be %q or %q", DataTypeIngress, DataTypeEgress)
	}
	if c.Bucket == "" {
		return fmt.Errorf("s3utils: S3_BUCKET is required")
	}
	if c.Namespace == "" {
		return fmt.Errorf("s3utils: TEST_NAMESPACE is required")
	}
	if c.TestName == "" {
		return fmt.Errorf("s3utils: TEST_NAME is required")
	}
	if c.SessionID == "" {
		return fmt.Errorf("s3utils: SESSION_ID is required")
	}
	return nil
}

// ObjectKeyPrefix returns shadow-diff/<ns>/<test>/sessions/<session>/<dataType>/.
func (c Config) ObjectKeyPrefix() string {
	return fmt.Sprintf("shadow-diff/%s/%s/sessions/%s/%s/",
		c.Namespace, c.TestName, c.SessionID, c.DataType)
}

// TestKeyPrefix returns the ShadowTest-level prefix (all sessions):
// shadow-diff/<namespace>/<test-name>/. Does not delete the bucket.
func TestKeyPrefix(namespace, testName string) string {
	return fmt.Sprintf("shadow-diff/%s/%s/", namespace, testName)
}

// RequireOperatingMode returns OPERATING_MODE; only record|replay are valid.
func RequireOperatingMode() (string, error) {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("OPERATING_MODE")))
	switch m {
	case ModeRecord, ModeReplay:
		return m, nil
	case "":
		return "", fmt.Errorf("s3utils: OPERATING_MODE is required (record|replay)")
	default:
		return "", fmt.Errorf("s3utils: OPERATING_MODE %q invalid (want record|replay)", m)
	}
}

// BatchUploader buffers JSON records and flushes them as JSON Lines to S3.
type BatchUploader struct {
	cfg    Config
	client ObjectPutter
	log    *slog.Logger

	mu   sync.Mutex
	buf  [][]byte
	stop chan struct{}
	done chan struct{}
}

// NewBatchUploader builds an uploader with a real S3 client (path-style when Endpoint set).
func NewBatchUploader(ctx context.Context, cfg Config, log *slog.Logger) (*BatchUploader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := newS3Putter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newBatchUploader(cfg, client, log), nil
}

// NewBatchUploaderWithPutter is for tests.
func NewBatchUploaderWithPutter(cfg Config, client ObjectPutter, log *slog.Logger) (*BatchUploader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("s3utils: ObjectPutter is required")
	}
	return newBatchUploader(cfg, client, log), nil
}

func newBatchUploader(cfg Config, client ObjectPutter, log *slog.Logger) *BatchUploader {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.FlushSize <= 0 {
		cfg.FlushSize = defaultFlushSize
	}
	if log == nil {
		log = slog.Default()
	}
	u := &BatchUploader{
		cfg:    cfg,
		client: client,
		log:    log,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go u.loop()
	return u
}

func newS3Putter(ctx context.Context, cfg Config) (ObjectPutter, error) {
	client, err := newS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &s3Putter{client: client, bucket: cfg.Bucket}, nil
}

type s3Putter struct {
	client *s3.Client
	bucket string
}

func (p *s3Putter) PutObject(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(p.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	return err
}

// Add JSON-encodes record and buffers it for the next flush.
func (u *BatchUploader) Add(record any) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("s3utils: marshal record: %w", err)
	}
	u.mu.Lock()
	u.buf = append(u.buf, raw)
	n := len(u.buf)
	flushNow := n >= u.cfg.FlushSize
	u.mu.Unlock()
	if flushNow {
		u.flush()
	}
	return nil
}

// Close stops the background flusher and uploads any remaining records.
func (u *BatchUploader) Close(ctx context.Context) error {
	select {
	case <-u.stop:
		// already closed
	default:
		close(u.stop)
	}
	select {
	case <-u.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (u *BatchUploader) loop() {
	defer close(u.done)
	t := time.NewTicker(u.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			u.flush()
			return
		case <-t.C:
			u.flush()
		}
	}
}

func (u *BatchUploader) flush() {
	u.mu.Lock()
	if len(u.buf) == 0 {
		u.mu.Unlock()
		return
	}
	batch := u.buf
	u.buf = nil
	u.mu.Unlock()

	var b strings.Builder
	for i, line := range batch {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.Write(line)
	}
	b.WriteByte('\n')

	name := fmt.Sprintf("%d-%s.jsonl", time.Now().UTC().UnixMilli(), uuid.NewString())
	key := u.cfg.ObjectKeyPrefix() + name

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := u.client.PutObject(ctx, key, []byte(b.String()), "application/x-ndjson"); err != nil {
		u.log.Error("s3 batch upload failed", "key", key, "records", len(batch), "err", err)
		// Re-queue so a transient failure is not silent data loss.
		u.mu.Lock()
		u.buf = append(batch, u.buf...)
		u.mu.Unlock()
		return
	}
	u.log.Info("s3 batch uploaded", "key", key, "records", len(batch))
}
