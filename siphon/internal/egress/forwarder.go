package egress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type RecordPayload struct {
	Method      string          `json:"method"`
	Host        string          `json:"host"`
	Path        string          `json:"path"`
	Body        json.RawMessage `json:"body"`
	IgnorePaths []string        `json:"ignore_paths,omitempty"`
	Response    RecordResponse  `json:"response"`
}

type RecordResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type Forwarder struct {
	client *http.Client
}

func NewForwarder() *Forwarder {
	return &Forwarder{
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (f *Forwarder) PostAsync(beruHTTPHost string, record RecordPayload) {
	go f.post(beruHTTPHost, record)
}

func (f *Forwarder) post(beruHTTPHost string, record RecordPayload) {
	if beruHTTPHost == "" {
		log.Printf("egress forwarder: beru_http_host not configured, dropping record")
		return
	}

	body := record.Body
	if len(body) == 0 {
		body = json.RawMessage("null")
	}
	if !json.Valid(body) {
		body = json.RawMessage(fmt.Sprintf("%q", string(body)))
	}

	payload := struct {
		Method      string          `json:"method"`
		Host        string          `json:"host"`
		Path        string          `json:"path"`
		Body        json.RawMessage `json:"body"`
		IgnorePaths []string        `json:"ignore_paths,omitempty"`
		Response    RecordResponse  `json:"response"`
	}{
		Method:      record.Method,
		Host:        record.Host,
		Path:        record.Path,
		Body:        body,
		IgnorePaths: record.IgnorePaths,
		Response:    record.Response,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("egress forwarder: marshal error: %v", err)
		return
	}

	url := fmt.Sprintf("http://%s/v1/record_egress", beruHTTPHost)
	resp, err := f.client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		log.Printf("egress forwarder: POST %s error: %v", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("egress forwarder: POST %s returned %s", url, resp.Status)
		return
	}
	log.Printf("egress forwarder: recorded %s %s%s -> %d", record.Method, record.Host, record.Path, record.Response.Status)
}
