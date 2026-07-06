package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MongoHints carries OTLP span attributes when db.query.text is absent or ambiguous.
type MongoHints struct {
	Operation  string // db.operation / db.operation.name
	Collection string // db.mongodb.collection / db.collection.name
}

var mongoCommandKeys = []string{
	"insert", "find", "update", "delete", "aggregate", "count", "distinct",
	"findAndModify", "createIndexes", "drop", "listIndexes", "getMore", "bulkWrite",
}

func EgressSignature(protocol string, payload []byte) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		if protocol == "mongodb" {
			return MongoSignature(payload, MongoHints{})
		}
		return fallbackSignature(protocol, payload)
	}
	switch protocol {
	case "mongodb":
		return MongoSignature(payload, MongoHints{})
	case "postgresql", "redis":
		return databaseSignature(protocol, obj)
	case "rabbitmq", "kafka":
		return queueSignature(protocol, obj)
	default:
		return fallbackSignature(protocol, payload)
	}
}

// MongoSignature derives mongodb:{operation}:{collection} from wire JSON and/or span hints.
func MongoSignature(payload []byte, hints MongoHints) string {
	hints = completeMongoHints(hints, payload)
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		if sig := mongoSignatureFromHints(hints); sig != "" {
			return sig
		}
		return fallbackSignature("mongodb", payload)
	}
	for _, cmd := range mongoCommandKeys {
		if col, ok := obj[cmd].(string); ok && col != "" {
			return fmt.Sprintf("mongodb:%s:%s", cmd, col)
		}
	}
	if sig := mongoSignatureFromHints(hints); sig != "" {
		return sig
	}
	if q, ok := obj["query"].(string); ok && q != "" && len(obj) == 1 {
		if sig := mongoSignatureFromQueryText(q); sig != "" {
			return sig
		}
	}
	return fallbackSignature("mongodb", mustJSON(obj))
}

func completeMongoHints(hints MongoHints, payload []byte) MongoHints {
	if hints.Operation == "" {
		var obj map[string]any
		if err := json.Unmarshal(payload, &obj); err == nil {
			hints.Operation = mongoOperationFromPayload(obj)
		}
	}
	return hints
}

func mongoOperationFromPayload(obj map[string]any) string {
	if q, ok := obj["query"].(string); ok && q != "" {
		fields := strings.Fields(strings.TrimSpace(q))
		if len(fields) > 0 {
			return normalizeMongoOperation(fields[0])
		}
	}
	return ""
}

func mongoOperationFromStatement(stmt string) string {
	return MongoOperationFromStatement(stmt)
}

// MongoOperationFromStatement extracts the command verb from pymongo-style db.statement text.
func MongoOperationFromStatement(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || strings.HasPrefix(stmt, "{") {
		return ""
	}
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return ""
	}
	return normalizeMongoOperation(fields[0])
}

func normalizeMongoOperation(op string) string {
	op = strings.ToLower(strings.TrimSpace(op))
	switch op {
	case "insertmany":
		return "insert"
	case "updatemany":
		return "update"
	case "deletemany":
		return "delete"
	default:
		return op
	}
}

func mongoSignatureFromHints(hints MongoHints) string {
	op := normalizeMongoOperation(hints.Operation)
	col := strings.TrimSpace(hints.Collection)
	if op != "" && col != "" {
		return fmt.Sprintf("mongodb:%s:%s", op, col)
	}
	return ""
}

func mongoSignatureFromQueryText(q string) string {
	fields := strings.Fields(strings.TrimSpace(q))
	if len(fields) < 2 {
		return ""
	}
	cmd := normalizeMongoOperation(fields[0])
	for _, k := range mongoCommandKeys {
		if cmd == k {
			return fmt.Sprintf("mongodb:%s:%s", cmd, fields[1])
		}
	}
	return ""
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
