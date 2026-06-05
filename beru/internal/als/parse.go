package als

import (
	"encoding/json"
	"fmt"
	"strings"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	tagShadowRole       = "x-shadow-role"
	mongoFilterMetadata = "envoy.filters.network.mongo_proxy"
	mongoRequestKey     = "request"
)

type parsedEntry struct {
	role         string
	connectionID string
	query        []byte
}

func parseTCPEntry(streamRole string, entry *accesslogv3.TCPAccessLogEntry) (parsedEntry, error) {
	var out parsedEntry
	if entry == nil {
		return out, fmt.Errorf("nil entry")
	}
	common := entry.GetCommonProperties()
	if common == nil {
		return out, fmt.Errorf("missing common properties")
	}
	if tags := common.GetCustomTags(); tags != nil {
		out.role = tags[tagShadowRole]
	}
	if out.role == "" {
		out.role = streamRole
	}
	if out.role == "" {
		return out, fmt.Errorf("missing x-shadow-role")
	}
	if sid := strings.TrimSpace(common.GetStreamId()); sid != "" {
		out.connectionID = sid
	}

	queryBytes, err := queryPayloadFromEntry(entry)
	if err != nil {
		return out, err
	}
	out.query = queryBytes
	if len(out.query) == 0 || string(out.query) == "-" {
		return out, fmt.Errorf("empty query payload")
	}
	return out, nil
}

func queryPayloadFromEntry(entry *accesslogv3.TCPAccessLogEntry) ([]byte, error) {
	if entry == nil {
		return nil, fmt.Errorf("nil entry")
	}
	if common := entry.GetCommonProperties(); common != nil {
		if meta := common.GetMetadata(); meta != nil {
			if b, ok := queryFromMetadata(meta); ok {
				return b, nil
			}
		}
	}
	if conn := entry.GetConnectionProperties(); conn != nil {
		payload, err := json.Marshal(map[string]uint64{
			"received_bytes": conn.GetReceivedBytes(),
			"sent_bytes":     conn.GetSentBytes(),
		})
		if err != nil {
			return nil, err
		}
		return payload, nil
	}
	return nil, fmt.Errorf("query not found in metadata")
}

func queryFromMetadata(meta *corev3.Metadata) ([]byte, bool) {
	if meta == nil {
		return nil, false
	}
	fm := meta.GetFilterMetadata()
	if fm == nil {
		return nil, false
	}
	st, ok := fm[mongoFilterMetadata]
	if !ok || st == nil {
		return nil, false
	}
	fields := st.GetFields()
	if fields == nil {
		return nil, false
	}
	if req, ok := fields[mongoRequestKey]; ok && req != nil {
		return valueToJSONBytes(req)
	}
	// emit_dynamic_metadata uses db.collection keys with operation list values.
	return structToJSONBytes(st)
}

func valueToJSONBytes(v *structpb.Value) ([]byte, bool) {
	switch v.GetKind().(type) {
	case *structpb.Value_StringValue:
		s := v.GetStringValue()
		if s == "" || s == "-" {
			return nil, false
		}
		b, err := normalizeQueryJSON([]byte(s))
		if err != nil {
			return nil, false
		}
		return b, true
	case *structpb.Value_StructValue:
		return structToJSONBytes(v.GetStructValue())
	case *structpb.Value_ListValue:
		raw, err := protojson.Marshal(v)
		if err != nil {
			return nil, false
		}
		b, err := normalizeQueryJSON(raw)
		if err != nil {
			return nil, false
		}
		return b, true
	default:
		raw, err := protojson.Marshal(v)
		if err != nil {
			return nil, false
		}
		return raw, true
	}
}

func structToJSONBytes(st *structpb.Struct) ([]byte, bool) {
	if st == nil {
		return nil, false
	}
	raw, err := protojson.Marshal(st)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func normalizeQueryJSON(body []byte) ([]byte, error) {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		wrapped, err := json.Marshal(map[string]string{"query": string(body)})
		if err != nil {
			return nil, err
		}
		return wrapped, nil
	}
	return json.Marshal(v)
}
