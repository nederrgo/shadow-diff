NODEJS_TEST_WORKER_IMG ?= nodejs-test-worker:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: docker-build

docker-build:
	docker build -t $(NODEJS_TEST_WORKER_IMG) .
