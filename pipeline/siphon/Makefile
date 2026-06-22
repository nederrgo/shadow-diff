SIPHON_IMG ?= siphon:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

.PHONY: build test docker-build run proto

proto:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc is required; install protobuf-compiler"; exit 1; }
	@command -v protoc-gen-go >/dev/null 2>&1 || go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	protoc \
		--go_out=internal/pbpacket --go_opt=paths=source_relative \
		--go-grpc_out=internal/pbpacket --go-grpc_opt=paths=source_relative \
		-I internal/pbpacket internal/pbpacket/packet.proto

build:
	CGO_ENABLED=0 go build -o bin/siphon ./cmd/siphon

test:
	CGO_ENABLED=0 go test -v ./...

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(SIPHON_IMG) .

run: build
	./bin/siphon
