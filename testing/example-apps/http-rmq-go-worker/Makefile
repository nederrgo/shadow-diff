HTTP_RMQ_GO_IMG ?= http-rmq-go-worker:dev
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build docker-build

build:
	CGO_ENABLED=0 go build -o bin/http-rmq-go-worker .

docker-build:
	docker build -t $(HTTP_RMQ_GO_IMG) .
