DB_TEST_IMG ?= db-test-app:dev
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

.PHONY: build test docker-build tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/db-test-app .

test:
	go test ./...

tidy:
	go mod tidy

docker-build:
	bash "$(REPO_ROOT)/scripts/lib/docker.sh" build -t $(DB_TEST_IMG) .
