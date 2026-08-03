package shadowspec

import "testing"

func TestResolveDependencyDefaults_KnownTypes(t *testing.T) {
	cases := []struct {
		typ   string
		image string
		port  int32
	}{
		{"rabbitmq", "rabbitmq:3-management-alpine", 5672},
		{"mongodb", "mongo:6.0", 27017},
		{"mongo", "mongo:6.0", 27017},
		{"redis", "redis:7-alpine", 6379},
		{"postgres", "postgres:16-alpine", 5432},
		{"postgresql", "postgres:16-alpine", 5432},
	}
	for _, tc := range cases {
		img, port := ResolveDependencyDefaults(tc.typ, "", 0)
		if img != tc.image || port != tc.port {
			t.Errorf("%s: got %s:%d want %s:%d", tc.typ, img, port, tc.image, tc.port)
		}
	}
}

func TestResolveDependencyDefaults_PreservesOverrides(t *testing.T) {
	img, port := ResolveDependencyDefaults("redis", "redis:6", 6380)
	if img != "redis:6" || port != 6380 {
		t.Fatalf("overrides lost: %s:%d", img, port)
	}
}

func TestResolveDependencyDefaults_UnknownLeavesEmpty(t *testing.T) {
	img, port := ResolveDependencyDefaults("mssql", "", 0)
	if img != "" || port != 0 {
		t.Fatalf("unknown type should not invent defaults, got %s:%d", img, port)
	}
}

func TestGetCatalog_NonEmpty(t *testing.T) {
	c := GetCatalog()
	if len(c.Dependencies) == 0 || len(c.Inputs) == 0 {
		t.Fatal("catalog missing entries")
	}
}

func TestContainsDriver(t *testing.T) {
	drivers := []string{DriverHTTPRequest, DriverRabbitMQMessage}
	if !ContainsDriver(drivers, DriverRabbitMQMessage) {
		t.Fatal("expected rabbitmq_message")
	}
	if !ContainsDriver(drivers, "  HTTP_REQUEST ") {
		t.Fatal("expected case-insensitive match")
	}
	if ContainsDriver(drivers, "kafka") {
		t.Fatal("unexpected kafka")
	}
	if ContainsDriver(nil, DriverHTTPRequest) {
		t.Fatal("empty list should miss")
	}
}
