package replay

import "testing"

// The seed path (POST /v1/record_egress) and the replay path (Envoy ext_proc)
// must derive byte-identical keys, or a recorded egress call is stored under a
// key nobody ever looks up and surfaces as a false "Egress Regression".
//
// Both sides call TraceKey(traceID, method, HostWithoutPort(host), path). This
// pins the two properties Kaisel relies on when it sends host, method and path
// verbatim off the wire: TraceKey normalizes the method itself, and the caller
// normalizes the host the same way on both sides.
func TestTraceKeyAgreesBetweenSeedAndLookup(t *testing.T) {
	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		path    = "/v1/users?active=true"
	)

	cases := []struct {
		name                     string
		seedMethod, lookupMethod string
		seedHost, lookupHost     string
	}{
		{
			name:       "identical inputs",
			seedMethod: "GET", lookupMethod: "GET",
			seedHost: "api.example.com", lookupHost: "api.example.com",
		},
		{
			// Kaisel sends the wire method; Envoy's :method is upper-case.
			// TraceKey upper-cases, so the two agree without either caller helping.
			name:       "method case is normalized by TraceKey",
			seedMethod: "get", lookupMethod: "GET",
			seedHost: "api.example.com", lookupHost: "api.example.com",
		},
		{
			// Kaisel sends Host with its port; Envoy's :authority may omit it.
			// HostWithoutPort on both sides reconciles them.
			name:       "host port is stripped on both sides",
			seedMethod: "POST", lookupMethod: "POST",
			seedHost: "api.example.com:8443", lookupHost: "api.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := TraceKey(traceID, tc.seedMethod, HostWithoutPort(tc.seedHost), path)
			lookup := TraceKey(traceID, tc.lookupMethod, HostWithoutPort(tc.lookupHost), path)
			if seed != lookup {
				t.Fatalf("key mismatch:\n  seed   = %s\n  lookup = %s", seed, lookup)
			}
		})
	}
}

// The query string is part of the key. Envoy's :path carries it, so a seeder
// that strips it stores a key the lookup can never produce.
func TestTraceKeyDistinguishesQueryString(t *testing.T) {
	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		host    = "api.example.com"
	)
	withQuery := TraceKey(traceID, "GET", host, "/search?q=cats")
	without := TraceKey(traceID, "GET", host, "/search")
	if withQuery == without {
		t.Fatal("query string must affect the key")
	}

	other := TraceKey(traceID, "GET", host, "/search?q=dogs")
	if withQuery == other {
		t.Fatal("different queries must not collide on one key")
	}
}

// Host case is NOT normalized by either side. That is why a seeder must send
// the host verbatim: lower-casing it would produce a key the ext_proc lookup
// can never generate from a mixed-case :authority.
//
// If this ever starts failing because HostWithoutPort gained a ToLower, the
// constraint is gone and seeders may safely normalize — update this test and
// the comment on export.EgressRecord together.
func TestTraceKeyIsHostCaseSensitive(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	lower := TraceKey(traceID, "GET", HostWithoutPort("api.example.com"), "/x")
	mixed := TraceKey(traceID, "GET", HostWithoutPort("API.Example.com"), "/x")
	if lower == mixed {
		t.Fatal("host case no longer distinguishes keys; seeder normalization rules changed")
	}
}
