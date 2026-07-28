SHOP_IMG ?= shop:latest

.PHONY: build test docker-build

build:
	go build ./...

test:
	go test ./...

docker-build:
	docker build -f Dockerfile -t $(SHOP_IMG) ..
