package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func EgressSignature(protocol string, payload []byte) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return fallbackSignature(protocol, payload)
	}
	switch protocol {
	case "mongodb", "postgresql", "redis":
		return databaseSignature(protocol, obj)
	case "rabbitmq", "kafka":
		return queueSignature(protocol, obj)
	default:
		return fallbackSignature(protocol, payload)
	}
}

func HTTPSignature(method, path string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if method == "" && path == "" {
		return "http:ingress:response"
	}
	if path == "" {
		path = "/"
	}
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("http:%s:%s", method, path)
}

func databaseSignature(protocol string, obj map[string]any) string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "$") {
			continue
		}
		if col, ok := obj[k].(string); ok && col != "" {
			return fmt.Sprintf("%s:%s:%s", protocol, k, col)
		}
	}
	return fallbackSignature(protocol, mustJSON(obj))
}

func queueSignature(protocol string, obj map[string]any) string {
	exchange := stringField(obj, "exchange")
	if exchange == "" {
		exchange = stringField(obj, "exchange_name")
	}
	routingKey := stringField(obj, "routing_key")
	if routingKey == "" {
		routingKey = stringField(obj, "routingKey")
	}
	if exchange != "" && routingKey != "" {
		return fmt.Sprintf("%s:publish:%s:%s", protocol, exchange, routingKey)
	}
	if routingKey != "" {
		return fmt.Sprintf("%s:publish:%s", protocol, routingKey)
	}
	if exchange != "" {
		return fmt.Sprintf("%s:publish:%s", protocol, exchange)
	}
	return fallbackSignature(protocol, mustJSON(obj))
}

func stringField(obj map[string]any, key string) string {
	v, ok := obj[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func fallbackSignature(protocol string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%s:unknown:%x", protocol, sum[:4])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
