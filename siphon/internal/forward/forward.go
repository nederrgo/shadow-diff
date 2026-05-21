package forward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/reassembly"
)

const headerShadowTraceID = "x-shadow-trace-id"

var defaultHopByHop = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Forwarder replays captured HTTP to Igris.
type Forwarder struct {
	log        *slog.Logger
	store      *config.Store
	client    *http.Client
	forwarded atomic.Uint64
}

func New(log *slog.Logger, store *config.Store) *Forwarder {
	if log == nil {
		log = slog.Default()
	}
	return &Forwarder{
		log:   log,
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (f *Forwarder) ForwardedCount() uint64 {
	return f.forwarded.Load()
}

// Run consumes parsed HTTP requests until ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context, in <-chan reassembly.ParsedHTTP) {
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-in:
			if !ok {
				return
			}
			if err := f.forwardOne(ctx, p); err != nil {
				f.log.Debug("Forward failed", "err", err, "dst", net.JoinHostPort(p.DstIP, strconv.Itoa(p.DstPort)))
			}
		}
	}
}

func (f *Forwarder) forwardOne(ctx context.Context, p reassembly.ParsedHTTP) error {
	payload := f.store.Get()
	if !f.shouldSample(payload.SampleRate, p.FlowKey) {
		return nil
	}
	route, ok := payload.LookupRoute(p.DstIP, p.DstPort)
	if !ok {
		return fmt.Errorf("no route for %s:%d", p.DstIP, p.DstPort)
	}

	outReq, err := buildOutboundRequest(p.Request, p.Body, route)
	if err != nil {
		return err
	}

	f.log.Info("Reassembled HTTP request",
		"method", outReq.Method,
		"path", outReq.URL.Path,
		"dst", net.JoinHostPort(p.DstIP, strconv.Itoa(p.DstPort)),
		"igris", outReq.URL.Host,
		"shadowtest", route.ShadowTestID,
	)

	req := outReq.WithContext(ctx)
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	f.forwarded.Add(1)
	return nil
}

func buildOutboundRequest(captured *http.Request, body []byte, route config.Route) (*http.Request, error) {
	path, rawQuery := pathAndQuery(captured)
	igrisHost := route.Target.IgrisHost
	port := route.IgrisPort
	hostPort := net.JoinHostPort(igrisHost, strconv.Itoa(port))
	fullURL := &url.URL{
		Scheme:   "http",
		Host:     hostPort,
		Path:     path,
		RawQuery: rawQuery,
	}

	outReq, err := http.NewRequest(captured.Method, fullURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	outReq.RequestURI = ""
	outReq.URL.Scheme = "http"
	outReq.URL.Host = hostPort
	outReq.Host = igrisHost
	outReq.ContentLength = int64(len(body))

	copyHeaders(outReq, captured.Header)
	StripHopByHop(outReq)
	if outReq.Header.Get(headerShadowTraceID) == "" {
		outReq.Header.Set(headerShadowTraceID, uuid.NewString())
	}
	return outReq, nil
}

func pathAndQuery(req *http.Request) (path, rawQuery string) {
	if req.URL != nil && req.URL.Path != "" {
		return req.URL.Path, req.URL.RawQuery
	}
	uri := req.RequestURI
	if uri == "" {
		return "/", ""
	}
	if i := strings.Index(uri, "?"); i >= 0 {
		return uri[:i], uri[i+1:]
	}
	return uri, ""
}

func copyHeaders(dst *http.Request, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Header.Add(k, v)
		}
	}
}

// StripHopByHop removes connection-level headers before client replay.
func StripHopByHop(req *http.Request) {
	connHeader := req.Header.Get("Connection")
	for _, h := range defaultHopByHop {
		req.Header.Del(h)
	}
	if connHeader != "" {
		for _, token := range strings.Split(connHeader, ",") {
			req.Header.Del(strings.TrimSpace(token))
		}
	}
	req.Header.Del("Connection")
	req.TransferEncoding = nil
	req.Close = false
}

func (f *Forwarder) shouldSample(rate int, flowKey string) bool {
	if rate >= 100 {
		return true
	}
	if rate <= 0 {
		return false
	}
	var h uint32
	for i := 0; i < len(flowKey); i++ {
		h = h*31 + uint32(flowKey[i])
	}
	return int(h%100) < rate
}
