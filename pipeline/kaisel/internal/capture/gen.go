// Package capture loads the eBPF collector and streams captured packets.
//
// This package is Linux-only. Everything portable -- and therefore everything
// with tests -- lives in internal/decode.
//
// The generated bindings and object are committed. Regenerating needs clang
// >= 12; `make verify-generate` proves the committed binding still matches
// capture.c. The compiler is taken from BPF2GO_CC.
package capture

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel -type pkt_meta bpf bpf/capture.c -- -I bpf -O2 -g -Wall -Werror
