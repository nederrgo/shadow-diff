package replay

import "testing"

func TestHashRequest_whitespaceInvariance(t *testing.T) {
	compact := []byte(`{"a":1}`)
	pretty := []byte("{\n  \"a\": 1\n}")

	h1, err := HashRequest("POST", "api.example.com", "/v1/foo", compact, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashRequest("POST", "api.example.com", "/v1/foo", pretty, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("expected same hash for equivalent JSON, got %q vs %q", h1, h2)
	}
}

func TestHashRequest_ignorePaths(t *testing.T) {
	body := []byte(`{"amount":100,"timestamp":"2024-01-01"}`)
	withIgnore, err := HashRequest("POST", "api.example.com", "/v1/orders", body, []string{"$.timestamp"})
	if err != nil {
		t.Fatal(err)
	}
	withoutIgnore, err := HashRequest("POST", "api.example.com", "/v1/orders", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withIgnore == withoutIgnore {
		t.Fatal("expected different hashes when ignore path present vs absent")
	}

	bodyNoTS := []byte(`{"amount":100}`)
	same, err := HashRequest("POST", "api.example.com", "/v1/orders", bodyNoTS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withIgnore != same {
		t.Fatalf("expected hash with ignored timestamp to match body without timestamp: %q vs %q", withIgnore, same)
	}
}

func TestHashRequest_nonJSON(t *testing.T) {
	body := []byte("plain text")
	h1, err := HashRequest("GET", "host", "/path", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashRequest("GET", "host", "/path", body, []string{"$.x"})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("non-JSON body should ignore paths")
	}
}

func TestHashRequest_methodHostPathAffectHash(t *testing.T) {
	body := []byte(`{}`)
	base, err := HashRequest("POST", "host", "/path", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, host, path string
	}{
		{"GET", "host", "/path"},
		{"POST", "other", "/path"},
		{"POST", "host", "/other"},
	}
	for _, c := range cases {
		h, err := HashRequest(c.method, c.host, c.path, body, nil)
		if err != nil {
			t.Fatal(err)
		}
		if h == base {
			t.Fatalf("expected different hash for %v", c)
		}
	}
}
