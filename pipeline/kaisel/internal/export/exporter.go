package export

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket"

	"github.com/shadow-diff/kaisel/internal/forwarder"
	"github.com/shadow-diff/kaisel/internal/sample"
)

const (
	defaultWorkers    = 8
	defaultQueue      = 1024
	defaultTimeout    = 5 * time.Second
	headerTraceparent = "traceparent"
)

// job is one queued forward. An egress job carries a non-nil egress payload
// and goes to Shop; otherwise it is an ingress request bound for igris.
type job struct {
	baseURL string
	record  forwarder.HTTPRecord
	egress  *EgressRecord
}

// Exporter admits captured requests and POSTs them to the matching igris.
type Exporter struct {
	router   *Router
	jobs     chan job
	wg       sync.WaitGroup
	dropped  atomic.Uint64
	log      *slog.Logger
	timeout  time.Duration
	stopOnce sync.Once
}

func NewExporter(router *Router, workers, queueSize int, log *slog.Logger) *Exporter {
	if router == nil {
		router = NewRouter(log)
	}
	if workers <= 0 {
		workers = defaultWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultQueue
	}
	if log == nil {
		log = slog.Default()
	}
	e := &Exporter{
		router:  router,
		jobs:    make(chan job, queueSize),
		log:     log,
		timeout: defaultTimeout,
	}
	for i := 0; i < workers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	return e
}

func (e *Exporter) Router() *Router { return e.router }

func (e *Exporter) Dropped() uint64 { return e.dropped.Load() }

func (e *Exporter) Stop() {
	e.stopOnce.Do(func() {
		close(e.jobs)
		e.wg.Wait()
	})
}

func (e *Exporter) worker() {
	defer e.wg.Done()
	clients := map[string]*forwarder.Client{}
	shops := map[string]*forwarder.ShopClient{}
	for j := range e.jobs {
		if j.egress != nil {
			e.recordEgress(shops, j)
			continue
		}
		c, ok := clients[j.baseURL]
		if !ok {
			var err error
			c, err = forwarder.NewClient(j.baseURL, e.timeout)
			if err != nil {
				e.log.Warn("igris client", "err", err, "base", j.baseURL)
				continue
			}
			clients[j.baseURL] = c
		}
		if err := c.Forward(context.Background(), j.record); err != nil {
			e.log.Warn("forward to igris failed", "err", err, "uri", j.record.RequestURI, "base", j.baseURL)
		}
	}
}

func (e *Exporter) recordEgress(shops map[string]*forwarder.ShopClient, j job) {
	c, ok := shops[j.baseURL]
	if !ok {
		var err error
		c, err = forwarder.NewShopClient(j.baseURL, e.timeout)
		if err != nil {
			e.log.Warn("shop client", "err", err, "base", j.baseURL)
			return
		}
		shops[j.baseURL] = c
	}
	hash, err := c.Record(context.Background(), j.egress)
	if err != nil {
		e.log.Warn("record egress to shop failed", "err", err,
			"method", j.egress.Method, "host", j.egress.Host, "path", j.egress.Path,
			"base", j.baseURL)
		return
	}
	// hash is Shop's own computed mock key, so logging it shows the key Envoy
	// will look up rather than one Kaisel guessed at.
	e.log.Info("egress recorded",
		"method", j.egress.Method, "host", j.egress.Host, "path", j.egress.Path,
		"status", j.egress.Response.Status, "body_bytes", len(j.egress.Response.Body),
		"hash", hash)
}

// Handle routes a captured request by destination IP, admits it, and enqueues a forward.
func (e *Exporter) Handle(netFlow gopacket.Flow, req *http.Request) {
	if req == nil {
		return
	}
	dst := netFlow.Dst().String()
	route, ok := e.router.Lookup(dst)
	if !ok || route.IgrisBaseURL == "" {
		return
	}

	tp := strings.TrimSpace(req.Header.Get(headerTraceparent))
	tid, ok := sample.TraceIDFromTraceparent(tp)
	if !ok {
		return
	}
	if !sample.SampledIn(tid, route.SamplePercentage) {
		return
	}

	body, _ := io.ReadAll(req.Body)
	record := forwarder.HTTPRecord{
		Method:      req.Method,
		RequestURI:  req.RequestURI,
		Host:        req.Host,
		Body:        body,
		Traceparent: tp,
		Headers:     forwarder.CloneRequestHeaders(req.Header),
	}

	select {
	case e.jobs <- job{baseURL: route.IgrisBaseURL, record: record}:
	default:
		// ponytail: drop on full queue; upgrade path = metric + larger queue
		e.dropped.Add(1)
	}
}

// WantTransaction reports whether a stream's connection needs response
// pairing, so streams with no egress route never buffer a response body.
func (e *Exporter) WantTransaction(netFlow gopacket.Flow) bool {
	route, ok := e.router.Lookup(netFlow.Src().String())
	return ok && route.EgressBaseURL != ""
}

// HandleTransaction records a paired egress request and response as a mock in
// the owning ShadowTest's Shop.
//
// The join key is the SOURCE address: a captured target pod acting as a client
// is making an egress call. (Ingress is the destination match, in Handle.) A
// connection between two target pods legitimately produces both — it is a real
// ingress for the receiver and a real egress for the caller.
func (e *Exporter) HandleTransaction(netFlow, transportFlow gopacket.Flow, req *http.Request, resp *http.Response) {
	if req == nil || resp == nil {
		return
	}
	route, ok := e.router.Lookup(netFlow.Src().String())
	if !ok || route.EgressBaseURL == "" {
		return
	}

	tp := strings.TrimSpace(req.Header.Get(headerTraceparent))
	tid, ok := sample.TraceIDFromTraceparent(tp)
	if !ok {
		// Tracing is required: a mock with no trace ID cannot be keyed, and
		// Shop rejects it anyway.
		return
	}
	if !sample.SampledIn(tid, route.SamplePercentage) {
		return
	}

	body, _ := io.ReadAll(resp.Body)
	record := &EgressRecord{
		TraceID: tid,
		Method:  req.Method,
		Host:    req.Host,
		Path:    req.RequestURI,
		Response: EgressResponse{
			Status:  resp.StatusCode,
			Headers: filterResponseHeaders(resp.Header),
			Body:    string(body),
		},
	}

	select {
	case e.jobs <- job{baseURL: route.EgressBaseURL, egress: record}:
	default:
		e.dropped.Add(1)
	}
}
