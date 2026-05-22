SIPHON_IMG ?= siphon:latest
CONTAINER_TOOL ?= docker

.PHONY: build test docker-build run

build:
	CGO_ENABLED=1 go build -o bin/siphon ./cmd/siphon

test:
	go test -v ./...

docker-build:
	$(CONTAINER_TOOL) build -t $(SIPHON_IMG) .

run: build
	./bin/siphon
