package capture

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/gopacket/layers"
	"github.com/shadow-diff/siphon/internal/config"
)

// debugTracer is set by CaptureManager for egress-layer summary logs.
var debugTracer *flowTracer

// SetDebugTracer wires capture stats into egress reassembly (debug only).
func SetDebugTracer(ft *flowTracer) {
	debugTracer = ft
}

// LogEgressCaptureSummary logs durable per-flow capture stats (survives log rotation).
func LogEgressCaptureSummary(egressFlowKey string) {
	if debugTracer != nil {
		debugTracer.logSummary(egressFlowKey)
	}
}

type dirCaptureStats struct {
	pkts      int
	dataPkts  int
	dataBytes int
}

// ponytail: debug-only per-flow counters for :80 egress; remove or gate once PCA truncation is fixed.
type flowTracer struct {
	mu      sync.Mutex
	counts  map[string]int
	byEgress map[string]*dirCaptureStats // keyed by egress flow + ":out" / ":in"
}

func newFlowTracer() *flowTracer {
	return &flowTracer{
		counts:   make(map[string]int),
		byEgress: make(map[string]*dirCaptureStats),
	}
}

func traceFlowKey(srcIP string, srcPort uint16, dstIP string, dstPort uint16) string {
	return fmt.Sprintf("%s:%d->%s:%d", srcIP, srcPort, dstIP, dstPort)
}

func egressFlowKey(m *config.Manager, srcIP, dstIP string, srcPort, dstPort uint16) (string, string) {
	if m.IsProdPodIP(srcIP) {
		return fmt.Sprintf("%s:%d-%s:%d", srcIP, srcPort, dstIP, dstPort), "out"
	}
	if m.IsProdPodIP(dstIP) {
		return fmt.Sprintf("%s:%d-%s:%d", dstIP, dstPort, srcIP, srcPort), "in"
	}
	return "", ""
}

func shouldTraceHTTPFlow(m *config.Manager, srcIP, dstIP string, srcPort, dstPort int) bool {
	if m == nil {
		return false
	}
	if dstPort == 80 && m.IsProdPodIP(srcIP) {
		return true
	}
	if srcPort == 80 && m.IsProdPodIP(dstIP) {
		return true
	}
	return false
}

func tcpFlags(tcp *layers.TCP) string {
	var b strings.Builder
	if tcp.SYN {
		b.WriteByte('S')
	}
	if tcp.ACK {
		b.WriteByte('A')
	}
	if tcp.PSH {
		b.WriteByte('P')
	}
	if tcp.FIN {
		b.WriteByte('F')
	}
	if tcp.RST {
		b.WriteByte('R')
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func (ft *flowTracer) log(m *config.Manager, srcIP, dstIP string, srcPort, dstPort uint16, tcp *layers.TCP) {
	if ft == nil || !shouldTraceHTTPFlow(m, srcIP, dstIP, int(srcPort), int(dstPort)) {
		return
	}
	dirKey := traceFlowKey(srcIP, srcPort, dstIP, dstPort)
	payload := len(tcp.Payload)
	egressKey, leg := egressFlowKey(m, srcIP, dstIP, srcPort, dstPort)

	ft.mu.Lock()
	ft.counts[dirKey]++
	n := ft.counts[dirKey]
	if egressKey != "" {
		sk := egressKey + ":" + leg
		st := ft.byEgress[sk]
		if st == nil {
			st = &dirCaptureStats{}
			ft.byEgress[sk] = st
		}
		st.pkts++
		if payload > 0 {
			st.dataPkts++
			st.dataBytes += payload
		}
	}
	ft.mu.Unlock()

	log.Printf("siphon debug: capture pkt flow=%s n=%d seq=%d ack=%d payload=%d flags=%s",
		dirKey, n, tcp.Seq, tcp.Ack, payload, tcpFlags(tcp))
}

func (ft *flowTracer) logSummary(egressFlowKey string) {
	ft.mu.Lock()
	out := ft.byEgress[egressFlowKey+":out"]
	in := ft.byEgress[egressFlowKey+":in"]
	ft.mu.Unlock()
	if out == nil && in == nil {
		return
	}
	outPkts, outData, outBytes := statsFields(out)
	inPkts, inData, inBytes := statsFields(in)
	log.Printf("siphon debug: capture summary flow=%s out_pkts=%d out_data_pkts=%d out_data_bytes=%d in_pkts=%d in_data_pkts=%d in_data_bytes=%d",
		egressFlowKey, outPkts, outData, outBytes, inPkts, inData, inBytes)
}

func statsFields(st *dirCaptureStats) (pkts, dataPkts, dataBytes int) {
	if st == nil {
		return 0, 0, 0
	}
	return st.pkts, st.dataPkts, st.dataBytes
}
