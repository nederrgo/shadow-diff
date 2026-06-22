package capture

import (
	"testing"

	"github.com/google/gopacket/layers"
	"github.com/shadow-diff/siphon/internal/config"
)

func TestShouldTraceHTTPFlow_prodEgressPort80(t *testing.T) {
	m := config.NewManager()
	m.Update(config.SiphonConfig{
		Targets: []config.SiphonTarget{{
			TargetIPs: []string{"10.0.0.1"},
		}},
	})
	if !shouldTraceHTTPFlow(m, "10.0.0.1", "10.0.0.2", 45678, 80) {
		t.Fatal("outbound prod->:80 should trace")
	}
	if !shouldTraceHTTPFlow(m, "10.0.0.2", "10.0.0.1", 80, 45678) {
		t.Fatal("inbound :80->prod should trace")
	}
	if shouldTraceHTTPFlow(m, "10.0.0.2", "10.0.0.3", 80, 443) {
		t.Fatal("non-prod flow should not trace")
	}
}

func TestFlowTracer_summary(t *testing.T) {
	m := config.NewManager()
	m.Update(config.SiphonConfig{
		Targets: []config.SiphonTarget{{TargetIPs: []string{"10.0.0.1"}}},
	})
	ft := newFlowTracer()
	ft.log(m, "10.0.0.1", "10.0.0.2", 57634, 80, &layers.TCP{BaseLayer: layers.BaseLayer{Payload: []byte("req")}})
	ft.log(m, "10.0.0.2", "10.0.0.1", 80, 57634, &layers.TCP{BaseLayer: layers.BaseLayer{Payload: []byte("headers")}})
	ft.log(m, "10.0.0.2", "10.0.0.1", 80, 57634, &layers.TCP{})
	ft.mu.Lock()
	out := ft.byEgress["10.0.0.1:57634-10.0.0.2:80:out"]
	in := ft.byEgress["10.0.0.1:57634-10.0.0.2:80:in"]
	ft.mu.Unlock()
	if out == nil || out.dataBytes != 3 || out.dataPkts != 1 {
		t.Fatalf("out=%+v", out)
	}
	if in == nil || in.dataBytes != 7 || in.dataPkts != 1 || in.pkts != 2 {
		t.Fatalf("in=%+v", in)
	}
}
