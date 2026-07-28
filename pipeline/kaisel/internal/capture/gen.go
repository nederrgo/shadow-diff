// Package capture loads the eBPF collector and streams captured packets.
//
// This package is Linux-only. Everything portable -- and therefore everything
// with tests -- lives in internal/decode.
//
// The generated bindings and object are committed. Regenerating needs clang
// >= 12; `make verify-generate` proves the committed binding still matches
// capture.c. The compiler is taken from BPF2GO_CC.
package capture

// Two builds of one source. The trace gate scans with bpf_loop, which lands in
// kernel 5.17; an unreachable bpf_loop still fails verification, so a runtime
// flag cannot switch the gate off and the fallback has to be a separate object.
// loadProgram tries them in order and takes the first the kernel accepts.
//
// -type pkt_meta on the gated build only: emitting the same Go type from both
// bindings would collide in one package.
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel -type pkt_meta bpf    bpf/capture.c -- -I bpf -O2 -g -Wall -Werror -DKAISEL_GATE_BPF_LOOP
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel            nogate bpf/capture.c -- -I bpf -O2 -g -Wall -Werror
