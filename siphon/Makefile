SIPHON_IMG ?= siphon:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

.PHONY: build test docker-build run

build:
	CGO_ENABLED=1 go build -o bin/siphon ./cmd/siphon

test:
	go test -v ./...

docker-build:
	bash "$(REPO_ROOT)/scripts/lib/docker.sh" build -t $(SIPHON_IMG) .

run: build
	./bin/siphon
