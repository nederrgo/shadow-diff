HTTP_MONGO_WORKER_IMG ?= http-mongo-worker:dev
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build docker-build

build:
	CGO_ENABLED=0 go build -o bin/http-mongo-worker .

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(HTTP_MONGO_WORKER_IMG) .
