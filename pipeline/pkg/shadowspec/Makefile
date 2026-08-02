.PHONY: test export-ts

test:
	CGO_ENABLED=0 go test ./...

# Run from repo root: make shadowspec-export
# Or: make -C pipeline/pkg/shadowspec export-ts REPO_ROOT=../../..
REPO_ROOT ?= ../../..
EXPORT_OUT ?= $(REPO_ROOT)/pipeline/the-system/src/lib/shadowCatalog.ts

export-ts:
	cd $(REPO_ROOT) && go run ./pipeline/pkg/shadowspec/cmd/export-ts $(EXPORT_OUT)
