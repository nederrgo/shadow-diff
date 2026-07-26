package export

import (
	"log/slog"
	"testing"
)

func TestRouter_Rebuild_firstKeyWins(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{
		{Key: "ns/b", IPs: []string{"10.0.0.1"}, IgrisBaseURL: "http://b:8080", SamplePercentage: 50},
		{Key: "ns/a", IPs: []string{"10.0.0.1"}, IgrisBaseURL: "http://a:8080", SamplePercentage: 10},
	})
	got, ok := r.Lookup("10.0.0.1")
	if !ok {
		t.Fatal("expected route")
	}
	if got.RuleKey != "ns/a" || got.IgrisBaseURL != "http://a:8080" || got.SamplePercentage != 10 {
		t.Fatalf("got %+v, want ns/a @ http://a:8080 sample=10", got)
	}
}

func TestRouter_Rebuild_skipsEmptyURL(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{
		{Key: "ns/a", IPs: []string{"10.0.0.1"}, IgrisBaseURL: ""},
	})
	if _, ok := r.Lookup("10.0.0.1"); ok {
		t.Fatal("empty URL should not register")
	}
}

func TestRouter_Rebuild_defaultSample(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{
		{Key: "ns/a", IPs: []string{"10.0.0.2"}, IgrisBaseURL: "http://a:8080"},
	})
	got, ok := r.Lookup("10.0.0.2")
	if !ok || got.SamplePercentage != 100 {
		t.Fatalf("got %+v", got)
	}
}
