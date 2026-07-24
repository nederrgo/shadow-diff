HTTP_RMQ_PYTHON_IMG ?= http-rmq-python-worker:dev
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: docker-build

docker-build:
	docker build -t $(HTTP_RMQ_PYTHON_IMG) .
