// Package shadowspec is the shared vocabulary for ShadowTest dependency kinds
// and ingress input drivers. Monarch uses it for image/port defaults; The System
// editor menus are generated from the same tables (make shadowspec-export).
package shadowspec

import "strings"

// DependencyKind describes one supported ephemeral dependency type.
type DependencyKind struct {
	Type            string   `json:"type"`
	Aliases         []string `json:"aliases,omitempty"`
	DefaultImage    string   `json:"defaultImage"`
	DefaultPort     int32    `json:"defaultPort"`
	SuggestedEnvVar string   `json:"suggestedEnvVar"`
	Label           string   `json:"label"`
}

// InputDriver describes one supported ingress driver and which fields it needs.
type InputDriver struct {
	Driver    string `json:"driver"`
	Label     string `json:"label"`
	NeedsPort bool   `json:"needsPort"`
	NeedsAMQP bool   `json:"needsAmqp"`
}

// Catalog is the static menu + defaults surface exported to The System.
type Catalog struct {
	Dependencies []DependencyKind `json:"dependencies"`
	Inputs       []InputDriver    `json:"inputs"`
}

var dependencyKinds = []DependencyKind{
	{
		Type:            "rabbitmq",
		DefaultImage:    "rabbitmq:3-management-alpine",
		DefaultPort:     5672,
		SuggestedEnvVar: "AMQP_URL",
		Label:           "RabbitMQ",
	},
	{
		Type:            "mongodb",
		Aliases:         []string{"mongo"},
		DefaultImage:    "mongo:6.0",
		DefaultPort:     27017,
		SuggestedEnvVar: "MONGO_URL",
		Label:           "MongoDB",
	},
	{
		Type:            "redis",
		DefaultImage:    "redis:7-alpine",
		DefaultPort:     6379,
		SuggestedEnvVar: "REDIS_ADDR",
		Label:           "Redis",
	},
	{
		Type:            "postgres",
		Aliases:         []string{"postgresql"},
		DefaultImage:    "postgres:16-alpine",
		DefaultPort:     5432,
		SuggestedEnvVar: "PG_DSN",
		Label:           "PostgreSQL",
	},
}

var inputDrivers = []InputDriver{
	{Driver: "http_request", Label: "HTTP request", NeedsPort: true},
	{Driver: "rabbitmq_message", Label: "RabbitMQ message", NeedsAMQP: true},
}

// DependencyKinds returns every dependency type the editor may offer.
func DependencyKinds() []DependencyKind {
	out := make([]DependencyKind, len(dependencyKinds))
	copy(out, dependencyKinds)
	return out
}

// InputDrivers returns every ingress driver the editor may offer.
func InputDrivers() []InputDriver {
	out := make([]InputDriver, len(inputDrivers))
	copy(out, inputDrivers)
	return out
}

// GetCatalog returns the full static catalog for export.
func GetCatalog() Catalog {
	return Catalog{
		Dependencies: DependencyKinds(),
		Inputs:       InputDrivers(),
	}
}

// LookupDependency finds a kind by type or alias (case-insensitive).
func LookupDependency(depType string) (DependencyKind, bool) {
	key := strings.ToLower(strings.TrimSpace(depType))
	for _, k := range dependencyKinds {
		if strings.ToLower(k.Type) == key {
			return k, true
		}
		for _, a := range k.Aliases {
			if strings.ToLower(a) == key {
				return k, true
			}
		}
	}
	return DependencyKind{}, false
}

// ResolveDependencyDefaults fills empty image/port from the catalog for depType.
// Unknown types leave image and port unchanged (caller-supplied values only).
func ResolveDependencyDefaults(depType, image string, port int32) (string, int32) {
	k, ok := LookupDependency(depType)
	if !ok {
		return image, port
	}
	if image == "" {
		image = k.DefaultImage
	}
	if port == 0 {
		port = k.DefaultPort
	}
	return image, port
}
