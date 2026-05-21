package reassembly

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"

	"github.com/shadow-diff/siphon/internal/config"
)

// ParsedHTTP is a reassembled HTTP request ready for forwarding.
type ParsedHTTP struct {
	Request *http.Request
	Body    []byte
	SrcIP   string
	DstIP   string
	DstPort int
	FlowKey string
}

// Hub runs tcpassembly and emits complete HTTP requests.
type Hub struct {
	log      *slog.Logger
	store    *config.Store
	out      chan<- ParsedHTTP
	reqCount uint64
	reqMu    sync.Mutex
}

func NewHub(log *slog.Logger, store *config.Store, out chan<- ParsedHTTP) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{log: log, store: store, out: out}
}

func (h *Hub) New(netFlow, tcpFlow gopacket.Flow) tcpassembly.Stream {
	return &stream{
		hub:     h,
		netFlow: netFlow,
		tcpFlow: tcpFlow,
		buffer:  &bytes.Buffer{},
	}
}

type stream struct {
	hub     *Hub
	netFlow gopacket.Flow
	tcpFlow gopacket.Flow
	buffer  *bytes.Buffer
}

func (s *stream) Reassembled(reassembly []tcpassembly.Reassembly) {
	for _, r := range reassembly {
		if len(r.Bytes) > 0 {
			s.buffer.Write(r.Bytes)
		}
	}
	s.tryParse()
}

func (s *stream) ReassemblyComplete() {
	s.tryParse()
}

func (s *stream) tryParse() {
	dstIP, dstPort := dstEndpoint(s.netFlow, s.tcpFlow)
	ports := s.hub.store.Get().HTTPListenerPorts()
	if _, ok := ports[dstPort]; !ok {
		return
	}

	for s.buffer.Len() > 0 {
		req, body, consumed, err := readHTTPRequest(s.buffer.Bytes())
		if err == io.ErrUnexpectedEOF {
			return
		}
		if err != nil {
			return
		}
		if consumed <= 0 {
			return
		}
		rest := s.buffer.Bytes()[consumed:]
		s.buffer.Reset()
		s.buffer.Write(rest)

		if req == nil {
			return
		}
		srcIP, _ := srcEndpoint(s.netFlow)
		s.hub.emit(ParsedHTTP{
			Request: req,
			Body:    body,
			SrcIP:   srcIP,
			DstIP:   dstIP,
			DstPort: dstPort,
			FlowKey: s.netFlow.String() + "/" + s.tcpFlow.String(),
		})
	}
}

func readHTTPRequest(data []byte) (*http.Request, []byte, int, error) {
	br := bytes.NewReader(data)
	r := bufio.NewReader(br)
	req, err := http.ReadRequest(r)
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, nil, 0, err
		}
		return nil, nil, 0, err
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, nil, 0, io.ErrUnexpectedEOF
	}
	consumed := len(data) - br.Len()
	return req, body, consumed, nil
}

func (h *Hub) emit(p ParsedHTTP) {
	h.reqMu.Lock()
	h.reqCount++
	h.reqMu.Unlock()
	select {
	case h.out <- p:
	default:
		h.log.Warn("Forward channel full; dropping request", "dst", net.JoinHostPort(p.DstIP, strconv.Itoa(p.DstPort)))
	}
}

// RequestCount returns reassembled HTTP requests emitted.
func (h *Hub) RequestCount() uint64 {
	h.reqMu.Lock()
	defer h.reqMu.Unlock()
	return h.reqCount
}

func dstEndpoint(netFlow, tcpFlow gopacket.Flow) (ip string, port int) {
	_, dst := netFlow.Endpoints()
	ip = endpointIP(dst)
	if tcpFlow.EndpointType() == layers.EndpointTCPPort {
		port = endpointPort(tcpFlow.Dst())
	}
	return ip, port
}

func srcEndpoint(netFlow gopacket.Flow) (ip string, port int) {
	src, _ := netFlow.Endpoints()
	ip = endpointIP(src)
	return ip, 0
}

func endpointIP(ep gopacket.Endpoint) string {
	if ep.EndpointType() == layers.EndpointIPv4 || ep.EndpointType() == layers.EndpointIPv6 {
		return net.IP(ep.Raw()).String()
	}
	return ep.String()
}

func endpointPort(ep gopacket.Endpoint) int {
	raw := ep.Raw()
	if len(raw) >= 2 {
		return int(binary.BigEndian.Uint16(raw))
	}
	return 0
}
