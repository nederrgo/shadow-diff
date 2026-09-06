package shadowspec

import "strings"

// Ingress driver names shared by Monarch status, the gRPC topology feed, and Tusk.
const (
	DriverHTTPRequest     = "http_request"
	DriverRabbitMQMessage = "rabbitmq_message"
)

// ContainsDriver reports whether drivers includes driver (case-insensitive, trimmed).
func ContainsDriver(drivers []string, driver string) bool {
	want := strings.ToLower(strings.TrimSpace(driver))
	if want == "" {
		return false
	}
	for _, d := range drivers {
		if strings.ToLower(strings.TrimSpace(d)) == want {
			return true
		}
	}
	return false
}
