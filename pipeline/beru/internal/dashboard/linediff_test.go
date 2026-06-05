package dashboard

import (
	"testing"
)

func TestRenderLineDiff_marksChanges(t *testing.T) {
	left, right := RenderLineDiff(
		[]byte(`{"a":1,"b":2}`),
		[]byte(`{"a":1,"b":3}`),
	)
	if len(left) == 0 || len(right) == 0 {
		t.Fatal("expected diff lines")
	}
	foundRed := false
	foundGreen := false
	for _, l := range left {
		if l.Class == "bg-red-100" {
			foundRed = true
		}
	}
	for _, r := range right {
		if r.Class == "bg-green-100" {
			foundGreen = true
		}
	}
	if !foundRed || !foundGreen {
		t.Fatalf("expected highlighted lines red=%v green=%v", foundRed, foundGreen)
	}
}

func TestRenderLineDiff_largePayload(t *testing.T) {
	large := []byte(`{"items":[` + repeatJSON(500) + `]}`)
	left, right := RenderLineDiff(large, large)
	if len(left) == 0 {
		t.Fatal("expected lines for large payload")
	}
	_ = right
}

func repeatJSON(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += `{"id":1}`
	}
	return s
}
