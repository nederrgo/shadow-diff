RECORDER_IMG ?= recorder:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

.PHONY: build test docker-build run

build:
	go build -o bin/recorder ./cmd/recorder

test:
	go test ./...

docker-build:
	bash "$(REPO_ROOT)/scripts/lib/docker.sh" build -t $(RECORDER_IMG) .

run: build
	./bin/recorder
