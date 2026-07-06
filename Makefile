# Shadow-Diff monorepo: delegate Monarch (operator) targets to pipeline/monarch/, Beru to pipeline/beru/.
MONARCH_DIR := pipeline/monarch
BERU_DIR := pipeline/beru
SHOP_DIR := pipeline/shop
IGRIS_DIR := pipeline/igrises/igris-http
SIPHON_DIR := pipeline/siphon
RECORDER_DIR := pipeline/recorder
IGRIS_RABBITMQ_DIR := pipeline/igrises/igris-rabbitmq
EGRESS_RELAY_RABBITMQ_DIR := pipeline/egress-relay-rabbitmq
RECORDER_IMG ?= recorder:latest
IGRIS_RABBITMQ_IMG ?= igris-rabbitmq:latest
EGRESS_RELAY_RABBITMQ_IMG ?= egress-relay-rabbitmq:latest
SIPHON_IMG ?= siphon:latest
IGRIS_IMG ?= igris-http:latest
BERU_IMG ?= beru:latest
SHOP_IMG ?= shop:latest
IMG ?= controller:latest

MONARCH_TARGETS := all help manifests generate fmt vet test setup-test-e2e test-e2e cleanup-test-e2e \
	lint lint-fix lint-config build run docker-build docker-push docker-buildx build-installer \
	install uninstall deploy undeploy kustomize controller-gen setup-envtest envtest golangci-lint \
	beru-docker-build beru-docker-push beru-proto

.PHONY: $(MONARCH_TARGETS) test-all
$(MONARCH_TARGETS):
	@$(MAKE) -C $(MONARCH_DIR) $(MAKECMDGOALS) IMG=$(IMG) BERU_IMG=$(BERU_IMG)

.PHONY: beru-test beru-build igris-test igris-build igris-docker-build \
	siphon-test siphon-build siphon-docker-build \
	recorder-test recorder-build recorder-docker-build \
	igris-rabbitmq-test igris-rabbitmq-build igris-rabbitmq-docker-build \
	nodejs-test-worker-docker-build python-test-worker-docker-build \
	egress-relay-rabbitmq-test egress-relay-rabbitmq-build egress-relay-rabbitmq-docker-build
beru-test: ## Run Beru unit tests.
	@$(MAKE) -C $(BERU_DIR) test

beru-build: ## Build Beru binary.
	@$(MAKE) -C $(BERU_DIR) build

shop-test: ## Run Shop unit tests.
	@$(MAKE) -C $(SHOP_DIR) test

shop-build: ## Build Shop binary.
	@$(MAKE) -C $(SHOP_DIR) build

shop-docker-build: ## Build Shop container image.
	@$(MAKE) -C $(SHOP_DIR) docker-build SHOP_IMG=$(SHOP_IMG)

igris-test: ## Run Igris unit tests.
	@$(MAKE) -C $(IGRIS_DIR) test

igris-build: ## Build Igris binary.
	@$(MAKE) -C $(IGRIS_DIR) build

igris-docker-build: ## Build Igris container image.
	@$(MAKE) -C $(IGRIS_DIR) docker-build IGRIS_IMG=$(IGRIS_IMG)

siphon-test: ## Run Siphon unit tests.
	@$(MAKE) -C $(SIPHON_DIR) test

siphon-build: ## Build Siphon agent binary.
	@$(MAKE) -C $(SIPHON_DIR) build

siphon-docker-build: ## Build Siphon container image.
	@$(MAKE) -C $(SIPHON_DIR) docker-build SIPHON_IMG=$(SIPHON_IMG)

recorder-test: ## Run Recorder unit tests.
	@$(MAKE) -C $(RECORDER_DIR) test

recorder-build: ## Build Recorder binary.
	@$(MAKE) -C $(RECORDER_DIR) build

recorder-docker-build: ## Build Recorder container image.
	@$(MAKE) -C $(RECORDER_DIR) docker-build RECORDER_IMG=$(RECORDER_IMG)

igris-rabbitmq-test: ## Run igris-rabbitmq unit tests.
	@$(MAKE) -C $(IGRIS_RABBITMQ_DIR) test

igris-rabbitmq-build: ## Build igris-rabbitmq binary.
	@$(MAKE) -C $(IGRIS_RABBITMQ_DIR) build

igris-rabbitmq-docker-build: ## Build igris-rabbitmq container image.
	@$(MAKE) -C $(IGRIS_RABBITMQ_DIR) docker-build IGRIS_RABBITMQ_IMG=$(IGRIS_RABBITMQ_IMG)

NODEJS_TEST_WORKER_DIR := testing/example-apps/nodejs-test-worker

nodejs-test-worker-docker-build: ## Build nodejs-test-worker container image.
	@$(MAKE) -C $(NODEJS_TEST_WORKER_DIR) docker-build NODEJS_TEST_WORKER_IMG=$(NODEJS_TEST_WORKER_IMG)

PYTHON_TEST_WORKER_DIR := testing/example-apps/python-test-worker
PYTHON_TEST_WORKER_IMG ?= python-test-worker:dev

python-test-worker-docker-build: ## Build python-test-worker container image.
	@$(MAKE) -C $(PYTHON_TEST_WORKER_DIR) docker-build PYTHON_TEST_WORKER_IMG=$(PYTHON_TEST_WORKER_IMG)

egress-relay-rabbitmq-test: ## Run egress-relay-rabbitmq unit tests.
	@$(MAKE) -C $(EGRESS_RELAY_RABBITMQ_DIR) test

egress-relay-rabbitmq-build: ## Build egress-relay-rabbitmq binary.
	@$(MAKE) -C $(EGRESS_RELAY_RABBITMQ_DIR) build

egress-relay-rabbitmq-docker-build: ## Build egress-relay-rabbitmq container image.
	@$(MAKE) -C $(EGRESS_RELAY_RABBITMQ_DIR) docker-build EGRESS_RELAY_RABBITMQ_IMG=$(EGRESS_RELAY_RABBITMQ_IMG)

test-all: ## Run Monarch, Beru, Shop, Igris, Siphon, Recorder, igris-rabbitmq, and egress-relay-rabbitmq tests.
	@$(MAKE) -C $(MONARCH_DIR) test
	@$(MAKE) -C $(BERU_DIR) test
	@$(MAKE) -C $(SHOP_DIR) test
	@$(MAKE) -C $(IGRIS_DIR) test
	@$(MAKE) -C $(SIPHON_DIR) test
	@$(MAKE) -C $(RECORDER_DIR) test
	@$(MAKE) -C $(IGRIS_RABBITMQ_DIR) test
	@$(MAKE) -C $(EGRESS_RELAY_RABBITMQ_DIR) test
.PHONY: test-bats test-bats-integration test-bats-e2e
test-bats-integration: ## Bats integration suite (one ShadowTest per .bats file).
	@chmod +x testing/bats/run.sh
	@./testing/bats/run.sh integration

test-bats-e2e: ## Bats E2E suite (shared env per file, multi-@test).
	@chmod +x testing/bats/run.sh
	@./testing/bats/run.sh e2e

test-bats: ## Run all Bats integration + E2E suites.
	@chmod +x testing/bats/run.sh
	@./testing/bats/run.sh all

# Run one .bats file or filtered @test. Examples:
#   make test-bats-one FILE=e2e/python_hybrid.bats
#   make test-bats-one FILE=e2e/python_hybrid.bats FILTER='RabbitMQ egress'
test-bats-one:
	@test -n "$(FILE)" || { echo "Usage: make test-bats-one FILE=e2e/python_hybrid.bats [FILTER='regex']"; exit 1; }
	@chmod +x testing/bats/run-one.sh
	@if [ -n "$(FILTER)" ]; then \
	  ./testing/bats/run-one.sh "$(FILE)" -f "$(FILTER)"; \
	else \
	  ./testing/bats/run-one.sh "$(FILE)"; \
	fi

.PHONY: log
log:
	@if [ -z "$(MSG)" ]; then echo "Error: Please provide a message. Example: make log MSG='Added service x'"; exit 1; fi
	@python3 scripts/log_change.py "$(MSG)"