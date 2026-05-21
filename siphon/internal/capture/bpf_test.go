package capture

import "testing"

func TestBuildBPFFilter(t *testing.T) {
	f, err := BuildBPFFilter([]string{"10.0.1.5", "10.0.1.6"}, []int{80, 8080})
	if err != nil {
		t.Fatal(err)
	}
	want := "tcp and (dst host 10.0.1.5 or dst host 10.0.1.6) and (dst port 80 or dst port 8080)"
	if f != want {
		t.Fatalf("got %q want %q", f, want)
	}
}

func TestBuildBPFFilterEmpty(t *testing.T) {
	if _, err := BuildBPFFilter(nil, []int{80}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildKernelBPFFilter(t *testing.T) {
	f, err := BuildKernelBPFFilter([]string{"10.244.0.92"}, []int{80})
	if err != nil {
		t.Fatal(err)
	}
	want := "tcp and (host 10.244.0.92) and (port 80)"
	if f != want {
		t.Fatalf("got %q want %q", f, want)
	}
}
