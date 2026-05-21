IGRIS_IMG ?= igris:latest
CONTAINER_TOOL ?= docker

.PHONY: build test docker-build run

build:
	go build -o bin/igris ./cmd/igris

test:
	go test ./...

docker-build:
	$(CONTAINER_TOOL) build -t $(IGRIS_IMG) .

run: build
	./bin/igris
