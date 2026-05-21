//go:build !kernel_bpf

package capture

import (
	"errors"

	"github.com/google/gopacket/layers"
	"golang.org/x/net/bpf"
)

var errKernelBPFUnavailable = errors.New("kernel BPF: build with -tags kernel_bpf and libpcap-dev")

func compileAndAttachBPF(tpacket interface {
	SetBPF(filter []bpf.RawInstruction) error
}, expr string, linkType layers.LinkType, snaplen int) error {
	return errKernelBPFUnavailable
}
