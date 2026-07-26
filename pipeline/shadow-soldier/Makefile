SHADOW_SOLDIER_IMG ?= shadow-soldier:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

.PHONY: build test docker-build run

build:
	go build -o bin/shadow-soldier ./cmd/shadow-soldier

test:
	go test ./...

docker-build:
	docker build -t $(SHADOW_SOLDIER_IMG) .

run: build
	./bin/shadow-soldier
