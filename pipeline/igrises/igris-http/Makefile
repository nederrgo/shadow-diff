IGRIS_IMG ?= igris-http:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build test docker-build run

build:
	go build -o bin/igris ./cmd/igris

test:
	go test ./...

docker-build:
	docker build -t $(IGRIS_IMG) .

run: build
	./bin/igris
