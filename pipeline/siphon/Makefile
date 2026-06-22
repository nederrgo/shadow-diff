SIPHON_IMG ?= siphon:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

.PHONY: build test docker-build run

build:
	CGO_ENABLED=0 go build -o bin/siphon ./cmd/siphon

test:
	CGO_ENABLED=0 go test ./...

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(SIPHON_IMG) .

run: build
	SIPHON_IGRIS_BASE_URL=http://127.0.0.1:8080 ./bin/siphon
