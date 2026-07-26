// Command kaisel captures HTTP traffic with eBPF and syncs target addresses
// from KaiselRule CRs via a controller-runtime manager.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/gopacket/gopacket"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/shadow-diff/kaisel/internal/capture"
	kaiselcontroller "github.com/shadow-diff/kaisel/internal/controller"
	"github.com/shadow-diff/kaisel/internal/decode"
	"github.com/shadow-diff/kaisel/internal/export"
	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(enginev1alpha1.AddToScheme(scheme))
}

// targets collects repeatable -target flags for manual/test runs.
type targets []net.IP

func (t *targets) String() string { return "" }
func (t *targets) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil || ip.To4() == nil {
			return &net.ParseError{Type: "IPv4 address", Text: s}
		}
		*t = append(*t, ip)
	}
	return nil
}

// ports collects repeatable -port flags for manual/test runs.
type ports []uint16

func (p *ports) String() string { return "" }
func (p *ports) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
		if err != nil || n == 0 {
			return fmt.Errorf("invalid TCP port %q", s)
		}
		*p = append(*p, uint16(n))
	}
	return nil
}

func main() {
	var tgts targets
	var prts ports
	iface := flag.String("iface", "lo", "interface to capture on")
	flag.Var(&tgts, "target", "IPv4 address to capture (repeatable, comma-separated); seeds initial BPF maps for manual runs")
	flag.Var(&prts, "port", "TCP port to capture (repeatable, comma-separated; empty = all ports)")
	l2Off := flag.Int("l2-off", -1, "link-layer header size: -1 autodetect, 0 raw L3, 14 Ethernet")
	perCPU := flag.Int("percpu-buffer", 0, "per-CPU perf ring bytes (0 = default)")
	logBodies := flag.Bool("log-bodies", false, "include request body content (truncated to 4KB) in logs; captured bodies are real production data")
	// Some k8s packages register -kubeconfig in init(); look it up rather than
	// re-registering to avoid the "flag redefined" panic.
	if flag.Lookup("kubeconfig") == nil {
		flag.String("kubeconfig", "", "path to kubeconfig (empty = in-cluster)")
	}
	namespace := flag.String("namespace", "", "namespace to scope KaiselRule watch (empty = all namespaces)")
	flag.Parse()
	kubeconfig := flag.Lookup("kubeconfig").Value.String()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	framing, err := resolveFraming(*iface, *l2Off)
	if err != nil {
		log.Error("resolve framing", "err", err)
		os.Exit(1)
	}

	ctx, stop := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		log.Info("shutting down")
		stop()
	}()

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Error("build REST config", "err", err)
		os.Exit(1)
	}

	mgrOpts := ctrl.Options{Scheme: scheme}
	if *namespace != "" {
		mgrOpts.Cache = ctrl.Options{}.Cache
		mgrOpts.Cache.DefaultNamespaces = map[string]cache.Config{*namespace: {}}
	}

	mgr, err := ctrl.NewManager(restCfg, mgrOpts)
	if err != nil {
		log.Error("create manager", "err", err)
		os.Exit(1)
	}

	updates := make(chan capture.MapUpdate, 16)
	exporter := export.NewExporter(export.NewRouter(log), 0, 0, log)
	defer exporter.Stop()

	if err := kaiselcontroller.New(mgr.GetClient(), updates, exporter.Router()).SetupWithManager(mgr); err != nil {
		log.Error("setup controller", "err", err)
		os.Exit(1)
	}

	cfg := capture.Config{
		Iface:           *iface,
		Targets:         tgts,
		Ports:           prts,
		Framing:         framing,
		PerCPUBuffer:    *perCPU,
		Log:             log,
		LogBodies:       *logBodies,
		Updates:         updates,
		OnTransaction:   exporter.HandleTransaction,
		WantTransaction: exporter.WantTransaction,
		OnRequest: func(netFlow, transportFlow gopacket.Flow, req *http.Request) {
			body, _ := io.ReadAll(req.Body)
			fields := []any{
				"src", netFlow.Src().String(), "dst", netFlow.Dst().String(),
				"ports", transportFlow.String(),
				"method", req.Method, "host", req.Host, "uri", req.RequestURI,
				"body_bytes", len(body),
			}
			if *logBodies {
				truncated := body
				const logBodyCap = 4096
				if len(truncated) > logBodyCap {
					truncated = truncated[:logBodyCap]
				}
				fields = append(fields, "body", string(truncated),
					"body_truncated", len(body) > logBodyCap)
			}
			log.Info("http request", fields...)
			req.Body = io.NopCloser(bytes.NewReader(body))
			exporter.Handle(netFlow, req)
		},
	}

	captureErr := make(chan error, 1)
	go func() { captureErr <- capture.Run(ctx, cfg) }()

	if err := mgr.Start(ctx); err != nil {
		log.Error("manager stopped", "err", err)
	}
	stop()

	if err := <-captureErr; err != nil {
		log.Error("capture stopped", "err", err)
		os.Exit(1)
	}
}

// resolveFraming honours an explicit -l2-off, else autodetects from sysfs.
// "any" (ifindex 0, every interface in the netns) has no single sysfs entry
// to read, so autodetect defaults to Ethernet -- true for eth0, veth, and
// bridge devices, the only ones a node actually multiplexes traffic across;
// loopback's differing layout is excluded by ifindex, not framing detection.
func resolveFraming(iface string, l2Off int) (decode.Framing, error) {
	switch l2Off {
	case -1:
		if iface == "any" {
			return decode.FramingEthernet, nil
		}
		return capture.DetectFraming(iface)
	case 0:
		return decode.FramingRawIP, nil
	case 14:
		return decode.FramingEthernet, nil
	default:
		return 0, &net.AddrError{Err: "l2-off must be -1, 0 or 14", Addr: "l2-off"}
	}
}
