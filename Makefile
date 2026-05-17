# Shadow-Diff monorepo: delegate Monarch (operator) targets to monarch/, Beru to beru/.
MONARCH_DIR := monarch
BERU_DIR := beru
BERU_IMG ?= beru:latest
IMG ?= controller:latest

MONARCH_TARGETS := all help manifests generate fmt vet test setup-test-e2e test-e2e cleanup-test-e2e \
	lint lint-fix lint-config build run docker-build docker-push docker-buildx build-installer \
	install uninstall deploy undeploy kustomize controller-gen setup-envtest envtest golangci-lint \
	beru-docker-build beru-docker-push beru-proto

.PHONY: $(MONARCH_TARGETS) test-all
$(MONARCH_TARGETS):
	@$(MAKE) -C $(MONARCH_DIR) $(MAKECMDGOALS) IMG=$(IMG) BERU_IMG=$(BERU_IMG)

.PHONY: beru-test beru-build
beru-test: ## Run Beru unit tests.
	@$(MAKE) -C $(BERU_DIR) test

beru-build: ## Build Beru binary.
	@$(MAKE) -C $(BERU_DIR) build

test-all: ## Run Monarch and Beru tests.
	@$(MAKE) -C $(MONARCH_DIR) test
	@$(MAKE) -C $(BERU_DIR) test
