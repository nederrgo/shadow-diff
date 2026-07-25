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
	defaultWorkers  = 8
	defaultQueue    = 1024
	defaultTimeout  = 5 * time.Second
	headerTraceparent = "traceparent"
)

type job struct {
	baseURL string
	record  forwarder.HTTPRecord
}

// Exporter admits captured requests and POSTs them to the matching igris.
type Exporter struct {
	router  *Router
	jobs    chan job
	wg      sync.WaitGroup
	dropped atomic.Uint64
	log     *slog.Logger
	timeout time.Duration
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
	for j := range e.jobs {
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
	}

	select {
	case e.jobs <- job{baseURL: route.IgrisBaseURL, record: record}:
	default:
		// ponytail: drop on full queue; upgrade path = metric + larger queue
		e.dropped.Add(1)
	}
}
