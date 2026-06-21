package consumer

import "testing"

func TestShouldReportEgress(t *testing.T) {
	tests := []struct {
		allow, exchange string
		want            bool
	}{
		{"egress-events", "egress-events", true},
		{"egress-events", "orders", false},
		{"egress-events", "", false},
		{"", "orders", true},
	}
	for _, tc := range tests {
		if got := shouldReportEgress(tc.allow, tc.exchange); got != tc.want {
			t.Fatalf("shouldReportEgress(%q, %q) = %v want %v", tc.allow, tc.exchange, got, tc.want)
		}
	}
}
