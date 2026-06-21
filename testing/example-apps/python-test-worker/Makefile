PYTHON_TEST_WORKER_IMG ?= python-test-worker:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: docker-build

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(PYTHON_TEST_WORKER_IMG) .
