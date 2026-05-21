//go:build kernel_bpf

package capture

import (
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"golang.org/x/net/bpf"
)

func compileAndAttachBPF(tpacket interface {
	SetBPF(filter []bpf.RawInstruction) error
}, expr string, linkType layers.LinkType, snaplen int) error {
	pcapBPF, err := pcap.CompileBPFFilter(linkType, snaplen, expr)
	if err != nil {
		return err
	}
	ins := make([]bpf.RawInstruction, len(pcapBPF))
	for i, in := range pcapBPF {
		ins[i] = bpf.RawInstruction{
			Op: in.Code,
			Jt: in.Jt,
			Jf: in.Jf,
			K:  in.K,
		}
	}
	return tpacket.SetBPF(ins)
}
