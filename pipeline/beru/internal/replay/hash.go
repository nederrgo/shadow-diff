package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// HashRequest computes a stable SHA-256 key from method, host, path, and normalized body.
// JSON bodies are stripped of ignorePaths then compacted to remove whitespace before hashing.
func HashRequest(method, host, path string, body []byte, ignorePaths []string) (string, error) {
	normalized, err := normalizeBody(body, ignorePaths)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(method))
	_, _ = h.Write([]byte(host))
	_, _ = h.Write([]byte(path))
	_, _ = h.Write(normalized)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeBody(body []byte, ignorePaths []string) ([]byte, error) {
	if len(body) == 0 {
		return []byte{}, nil
	}
	if !json.Valid(body) {
		return body, nil
	}
	stripped, err := stripJSONPaths(body, ignorePaths)
	if err != nil {
		return nil, err
	}
	return compactJSON(stripped)
}
