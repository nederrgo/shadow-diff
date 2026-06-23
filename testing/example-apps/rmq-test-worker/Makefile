RMQ_TEST_WORKER_IMG ?= rmq-test-worker:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build docker-build

build:
	CGO_ENABLED=0 go build -o bin/rmq-test-worker .

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(RMQ_TEST_WORKER_IMG) .
