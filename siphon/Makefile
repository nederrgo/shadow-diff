SIPHON_IMG ?= siphon:latest
CONTAINER_TOOL ?= docker

.PHONY: build test docker-build run

build:
	CGO_ENABLED=1 go build -tags pcap,kernel_bpf -o bin/siphon-agent ./cmd/agent

test:
	go test ./...

docker-build:
	$(CONTAINER_TOOL) build -t $(SIPHON_IMG) .

run: build
	./bin/siphon-agent
