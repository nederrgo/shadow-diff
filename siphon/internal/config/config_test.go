package config

import "testing"

func TestPayloadValidate(t *testing.T) {
	p := Payload{
		SampleRate: 100,
		Targets: []Target{{
			ShadowTest:  "default/st",
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []int{8080},
			IgrisHost:   "igris.shadow.svc.cluster.local",
			Listeners:   []Listener{{Port: 8080, Driver: "http_request"}},
		}},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnionCaptureTargets(t *testing.T) {
	p := Payload{
		Targets: []Target{
			{TargetIPs: []string{"10.0.0.1", "10.0.0.2"}, TargetPorts: []int{80}},
			{TargetIPs: []string{"10.0.0.2"}, TargetPorts: []int{8080}},
		},
	}
	ips, ports := p.UnionCaptureTargets()
	if len(ips) != 2 || len(ports) != 2 {
		t.Fatalf("got ips=%v ports=%v", ips, ports)
	}
}

func TestLookupRoute(t *testing.T) {
	p := Payload{
		Targets: []Target{{
			ShadowTest:  "ns/st",
			TargetIPs:   []string{"10.1.1.2"},
			TargetPorts: []int{8080},
			IgrisHost:   "igris.ns.svc.cluster.local",
			Listeners:   []Listener{{Port: 8080, Driver: "http_request"}},
		}},
	}
	_, ok := p.LookupRoute("10.1.1.2", 8080)
	if !ok {
		t.Fatal("expected route")
	}
}
