EGRESS_RELAY_RABBITMQ_IMG ?= egress-relay-rabbitmq:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

.PHONY: build test docker-build run

build:
	go build -o bin/egress-relay-rabbitmq ./cmd/egress-relay-rabbitmq

test:
	go test ./...

docker-build:
	bash "$(REPO_ROOT)/scripts/lib/docker.sh" build -t $(EGRESS_RELAY_RABBITMQ_IMG) .

run: build
	./bin/egress-relay-rabbitmq
