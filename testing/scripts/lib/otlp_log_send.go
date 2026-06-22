//go:build ignore

// otlp_log_send posts one OTLP log record to Siphon (local smoke only).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:4317", "OTLP gRPC address")
	trace := flag.String("trace", "", "x-shadow-trace-id (required)")
	method := flag.String("method", "GET", "HTTP method")
	path := flag.String("path", "/", "request path")
	body := flag.String("body", "", "optional body")
	flag.Parse()
	if strings.TrimSpace(*trace) == "" {
		fmt.Fprintln(os.Stderr, "error: -trace required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	attrs := []*commonpb.KeyValue{
		kv("http.request.method", *method),
		kv("url.path", *path),
		kv("x-shadow-trace-id", *trace),
	}
	var bodyVal *commonpb.AnyValue
	if b := []byte(*body); len(b) > 0 {
		bodyVal = &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: b}}
	}
	req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
			Attributes: attrs, Body: bodyVal,
		}}}},
	}}}
	if _, err := collogspb.NewLogsServiceClient(conn).Export(ctx, req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func kv(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: val}}}
}
