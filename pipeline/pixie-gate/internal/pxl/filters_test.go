package pxl

import (
	"strings"
	"testing"
)

func TestSampleFilterLines_100(t *testing.T) {
	got := SampleFilterLines(100, "trace_hdr")
	if got != "df = df[df.trace_hdr != '']" {
		t.Fatalf("got %q", got)
	}
}

func TestSampleFilterLines_zero(t *testing.T) {
	got := SampleFilterLines(0, "trace_hdr")
	if got != "df = df[df.trace_hdr != '']" {
		t.Fatalf("got %q", got)
	}
}

func TestSampleFilterLines_10(t *testing.T) {
	got := SampleFilterLines(10, "trace_hdr")
	want := []string{
		"df = df[df.trace_hdr != '']",
		"df._h0 = px.tolower(px.substring(df.trace_hdr, 3, 1))",
		"df._h1 = px.tolower(px.substring(df.trace_hdr, 4, 1))",
		"df._n0 = px.select",
		"df._n1 = px.select",
		"df = df[df._n0 >= 0]",
		"df = df[df._n1 >= 0]",
		"df._sample_v = df._n0 * 16 + df._n1",
		"df = df[(df._sample_v * 100) < (10 * 256)]",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "px.atoi") {
		t.Fatal("must not use px.atoi (second arg is default, not radix)")
	}
}

func TestHexNibbleExpr(t *testing.T) {
	got := HexNibbleExpr("df._h0")
	for _, w := range []string{
		"px.select(df._h0 == '0', 0,",
		"px.select(df._h0 == 'a', 10,",
		"px.select(df._h0 == 'f', 15,",
		", -1)",
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in %s", w, got)
		}
	}
}

func TestSampleFilterLines_column(t *testing.T) {
	got := SampleFilterLines(25, "traceparent")
	if !strings.Contains(got, "df.traceparent") {
		t.Fatalf("expected traceparent column: %s", got)
	}
	if !strings.Contains(got, "(25 * 256)") {
		t.Fatalf("expected pct scaling: %s", got)
	}
}

func TestLabelFilterLines_app(t *testing.T) {
	got := LabelFilterLines(map[string]string{"app": "worker"})
	if got != "df = df[px.contains(df.pod, 'worker')]" {
		t.Fatalf("got %q", got)
	}
}

func TestPortFilterLines_min(t *testing.T) {
	got := PortFilterLines([]int32{8888, 80})
	if got != "df = df[df.local_port == 80]" {
		t.Fatalf("got %q", got)
	}
}
