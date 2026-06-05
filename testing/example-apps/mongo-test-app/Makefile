MONGO_TEST_IMG ?= mongo-test-app:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: build test docker-build tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/mongo-test-app .

test:
	go test ./...

tidy:
	go mod tidy

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(MONGO_TEST_IMG) .
