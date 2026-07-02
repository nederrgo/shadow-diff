package beru

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// RecordPayload is the JSON body for POST /v1/record_egress.
type RecordPayload struct {
	TraceID  string         `json:"trace_id"`
	Method   string         `json:"method"`
	Host     string         `json:"host"`
	Path     string         `json:"path"`
	Response RecordResponse `json:"response"`
}

// RecordResponse is the recorded HTTP response half.
type RecordResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Client posts egress records to Beru.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a Beru HTTP client. baseURL is e.g. http://beru.beru-system.svc.cluster.local:8080.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// PostAsync sends a record in the background.
func (c *Client) PostAsync(record RecordPayload) {
	go c.post(record)
}

func (c *Client) post(record RecordPayload) {
	raw, err := json.Marshal(record)
	if err != nil {
		log.Printf("beru client: marshal error: %v", err)
		return
	}

	url := c.baseURL + "/v1/record_egress"
	resp, err := c.http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		log.Printf("beru client: POST %s error: %v", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("beru client: POST %s returned %s", url, resp.Status)
		return
	}
	log.Printf("beru client: recorded %s %s%s -> %d", record.Method, record.Host, record.Path, record.Response.Status)
}
