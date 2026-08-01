PROTO_SRC = monarch.proto

.PHONY: proto build test

proto:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc is required; install protobuf-compiler"; exit 1; }
	@command -v protoc-gen-go >/dev/null 2>&1 || go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		-I . $(PROTO_SRC)

build:
	CGO_ENABLED=0 go build ./...

test:
	CGO_ENABLED=0 go test ./...
