IGRIS_RABBITMQ_IMG ?= igris-rabbitmq:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build test docker-build run

build:
	go build -o bin/igris-rabbitmq ./cmd/igris-rabbitmq

test:
	go test ./...

docker-build:
	docker build -t $(IGRIS_RABBITMQ_IMG) -f Dockerfile $(REPO_ROOT)

run: build
	./bin/igris-rabbitmq
