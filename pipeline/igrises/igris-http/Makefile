IGRIS_IMG ?= igris:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build test docker-build run

build:
	go build -o bin/igris ./cmd/igris

test:
	go test ./...

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(IGRIS_IMG) .

run: build
	./bin/igris
