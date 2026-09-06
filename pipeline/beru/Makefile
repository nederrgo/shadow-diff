BERU_IMG ?= beru:latest
CONTAINER_TOOL ?= docker
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

PROTO_SRC = api/proto/beru/v1/traffic.proto
PROTO_OUT = pkg/api/beru/v1

.PHONY: proto build test docker-build

proto:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc is required; install protobuf-compiler"; exit 1; }
	@command -v protoc-gen-go >/dev/null 2>&1 || go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	mkdir -p $(PROTO_OUT)
	protoc \
		--go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
		-I api/proto/beru/v1 $(PROTO_SRC)

build:
	CGO_ENABLED=0 go build -o bin/beru ./cmd/beru

test:
	CGO_ENABLED=0 go test ./...

docker-build:
	docker build -f Dockerfile -t $(BERU_IMG) ..
