TUSK_IMG ?= tusk:latest

.PHONY: build test docker-build run

build:
	CGO_ENABLED=0 go build -o bin/tusk ./cmd/tusk

test:
	CGO_ENABLED=0 go test ./...

docker-build:
	docker build -f Dockerfile -t $(TUSK_IMG) ..

run: build
	./bin/tusk
