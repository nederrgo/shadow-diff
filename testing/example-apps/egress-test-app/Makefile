EGRESS_TEST_IMG ?= egress-test-app:dev
CONTAINER_TOOL ?= docker

.PHONY: build test docker-build tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/egress-test-app .

test:
	go test ./...

tidy:
	go mod tidy

docker-build:
	$(CONTAINER_TOOL) build -t $(EGRESS_TEST_IMG) .
