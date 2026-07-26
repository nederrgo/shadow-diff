KAISEL_IMG ?= kaisel:latest
CONTAINER_TOOL ?= docker
# BPF object bytes are not stable across clang major/patch versions. Keep this
# aligned with whatever produced the committed artifacts (see verify-generate).
BPF2GO_CC ?= clang-18

.PHONY: build test test-integration generate verify-generate run

build:
	CGO_ENABLED=0 go build -o bin/kaisel ./cmd/kaisel

test:
	CGO_ENABLED=0 go test ./...

# Needs root (CAP_BPF + CAP_NET_RAW), iproute2, python3 and openssl. Builds a
# bridge and two network namespaces, loads the real BPF program, and drives real
# traffic. Deliberately excluded from `test` and CI, which stay unprivileged.
test-integration:
	go test -tags integration -count=1 -v -timeout 10m ./internal/capture/...

generate:
	BPF2GO_CC=$(BPF2GO_CC) go generate ./...

# Diffs the generated Go binding only, never the .o: object bytes differ across
# clang patch versions, so a strict .o diff fails on a clean tree. The binding
# changes whenever maps, pkt_meta or .rodata variables change -- the drift that
# actually breaks the Go/C contract. The trailing build proves the regenerated
# object still compiles and embeds.
verify-generate: generate
	git diff --exit-code -- internal/capture/bpf_bpfel.go
	go build ./...

docker-build:
	$(CONTAINER_TOOL) build -t $(KAISEL_IMG) -f Dockerfile ../..

run: build
	./bin/kaisel -iface lo -target 127.0.0.1
