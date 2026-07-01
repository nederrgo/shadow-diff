NODEJS_HYBRID_WORKER_IMG ?= nodejs-hybrid-worker:dev
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: docker-build

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(NODEJS_HYBRID_WORKER_IMG) .
