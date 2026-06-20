.DEFAULT_GOAL := help

PROTO_DIR_ENGINE     := plugins/engine/api/proto
PROTO_DIR_DB         := plugins/db/api/proto
PROTO_DIR_MEMORY     := plugins/memory/api/proto
PROTO_DIR_INSTINCT   := plugins/instinct/api/proto
PROTO_DIR_CAPABILITY := plugins/capability/api/proto
PROTO_DIR_EVALUATOR  := plugins/evaluator/api/proto

# protogen builds one proto package; the per-plugin `proto-*` targets
# below pass the right include dir and source file. Keeps the proto
# regeneration target idempotent across plugin add/drop cycles.
define protogen
	protoc -I $(1) \
		--go_out=$(1) --go_opt=paths=source_relative \
		--go-grpc_out=$(1) --go-grpc_opt=paths=source_relative \
		$(1)/$(2).proto
endef


.PHONY: proto
proto:  ## Regenerate gRPC stubs for every plugin contract.
	$(call protogen,$(PROTO_DIR_ENGINE),engine)
	$(call protogen,$(PROTO_DIR_MEMORY),memory)
	$(call protogen,$(PROTO_DIR_INSTINCT),instinct)
	$(call protogen,$(PROTO_DIR_CAPABILITY),capability)
	$(call protogen,$(PROTO_DIR_EVALUATOR),evaluator)
	# v0.4.0 transition: regenerate the legacy db.proto too while the
	# old plugins/db/ tree still exists (deleted in Λ-7.1). After that
	# this line goes away.
	$(call protogen,$(PROTO_DIR_DB),db)


.PHONY: test
test:  ## Run all unit tests with the race detector (skips integration).
	go test ./... -race


.PHONY: test-short
test-short:  ## Run unit tests only (skip integration / -tags=integration).
	go test ./... -short -race


.PHONY: integration-test
integration-test: build  ## Run real-mysqld E2E (needs Nix + ~30-60s mysqld warmup).
	PATH=$(CURDIR)/dist:$$PATH go test -tags=integration -timeout=10m -v ./tests/integration/...


.PHONY: lint
lint:  ## golangci-lint run.
	golangci-lint run ./...


.PHONY: fmt
fmt:  ## gofumpt + gci.
	gofumpt -w .
	gci write --skip-generated .


.PHONY: build
build:  ## Build host + all engine plugins + memory plugins under dist/.
	mkdir -p dist
	go build -o dist/bough ./cmd/bough
	go build -o dist/bough-plugin-mysql ./cmd/bough-plugin-mysql
	go build -o dist/bough-plugin-postgres ./cmd/bough-plugin-postgres
	go build -o dist/bough-plugin-redis ./cmd/bough-plugin-redis
	go build -o dist/bough-plugin-elasticsearch ./cmd/bough-plugin-elasticsearch
	go build -o dist/bough-plugin-memory-sqlite ./cmd/bough-plugin-memory-sqlite


# PLUGIN is the engine kind: mysql / postgres / redis / elasticsearch.
# CI does the same pre-pull + build + run per matrix cell; this target
# is what plugin authors (internal or external) run on their laptop
# before opening a PR.
PLUGIN ?= mysql

.PHONY: conformance-local
conformance-local: build  ## Run the conformance suite locally against PLUGIN=<kind>.
	BOUGH_CONFORMANCE_PLUGIN_BIN=$(CURDIR)/dist/bough-plugin-$(PLUGIN) \
		go test -tags=conformance -race -timeout=15m -v ./plugins/engine/$(PLUGIN)/...


.PHONY: conformance-all
conformance-all: build  ## Run conformance against all four bough-internal plugins.
	@for kind in mysql postgres redis elasticsearch; do \
		echo "=== conformance: $$kind ==="; \
		BOUGH_CONFORMANCE_PLUGIN_BIN=$(CURDIR)/dist/bough-plugin-$$kind \
			go test -tags=conformance -race -timeout=15m -v \
				./plugins/engine/$$kind/... || exit 1; \
	done


.PHONY: clean
clean:  ## Remove build artefacts.
	rm -rf dist


.PHONY: help
help:  ## Show all targets.
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?##"}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
