package capture

import (
	"bufio"
	"net"
	"os"
	"strings"
)

// AllInterfacesLabel is the status/API name for the capture mode covering all ifaces.
const AllInterfacesLabel = "all"

// ResolveInterfaceMode returns explicit iface name or multi-iface (any) mode.
func ResolveInterfaceMode(env string) (explicit string, multi bool) {
	v := strings.TrimSpace(strings.ToLower(env))
	switch v {
	case "", "any", "auto":
		return "", true
	default:
		return env, false
	}
}

// includeInterface reports whether to attach AF_PACKET on this iface in "any" mode.
func includeInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	if len(iface.HardwareAddr) > 0 {
		return true
	}
	// Kind/CNI bridges often have no MAC but carry pod-to-pod traffic.
	name := iface.Name
	return name == "cni0" || strings.HasPrefix(name, "kindnet") || strings.HasPrefix(name, "br-")
}

// ListCaptureInterfaces returns active non-loopback interfaces for multi-iface capture.
func ListCaptureInterfaces() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, iface := range ifaces {
		if includeInterface(iface) {
			names = append(names, iface.Name)
		}
	}
	return names, nil
}

// InterfaceEnv reads SIPHON_INTERFACE from the environment.
func InterfaceEnv() string {
	return os.Getenv("SIPHON_INTERFACE")
}

// FindIfacesForIPs reads /proc/net/arp and returns the set of veth/host interfaces
// that serve any of the given IP addresses.  Each target pod's veth appears in the
// ARP table of the host (Kind node) network namespace that Siphon runs in.
// Deduplication is applied so one AF_PACKET socket per physical interface is opened.
func FindIfacesForIPs(ips []string) ([]string, error) {
	ipSet := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		ipSet[ip] = struct{}{}
	}

	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]struct{})
	var result []string
	sc := bufio.NewScanner(f)
	sc.Scan() // skip header line
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// /proc/net/arp columns: IP, HW_type, Flags, HW_addr, Mask, Device
		if len(fields) < 6 {
			continue
		}
		ip, dev := fields[0], fields[5]
		if _, ok := ipSet[ip]; !ok {
			continue
		}
		if _, dup := seen[dev]; dup {
			continue
		}
		seen[dev] = struct{}{}
		result = append(result, dev)
	}
	return result, sc.Err()
}
