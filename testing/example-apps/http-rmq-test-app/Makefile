HTTP_RMQ_TEST_IMG ?= http-rmq-test-app:dev
REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

.PHONY: docker-build

docker-build:
	bash "$(REPO_ROOT)/testing/scripts/lib/docker.sh" build -t $(HTTP_RMQ_TEST_IMG) .
