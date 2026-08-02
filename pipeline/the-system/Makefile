THE_SYSTEM_IMG ?= the-system:latest

# Requires Node >= 20 (Vite 6). Dockerfile builds with node:22-alpine.
.PHONY: test typecheck build docker-build

typecheck:
	npm ci
	npm run typecheck

test: typecheck ## Typecheck The System UI.

build:
	npm ci
	npm run build

docker-build: ## Build The System Nginx image.
	docker build -t $(THE_SYSTEM_IMG) .
