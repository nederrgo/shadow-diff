PIXIE_GATE_IMG ?= pixie-gate:latest
CONTAINER_TOOL ?= docker

.PHONY: build test docker-build

build:
	go build -o bin/pixie-gate ./cmd/pixie-gate

test:
	go test ./...

docker-build:
	$(CONTAINER_TOOL) build -t $(PIXIE_GATE_IMG) .
