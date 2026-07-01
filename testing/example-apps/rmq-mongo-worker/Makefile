RMQ_MONGO_WORKER_IMG ?= rmq-mongo-worker:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build docker-build

build:
	CGO_ENABLED=0 go build -o bin/rmq-mongo-worker .

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(RMQ_MONGO_WORKER_IMG) .
