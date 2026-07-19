# LLMObs — the single task entry point.
#
# Contributors, CI, and coding agents all use these verbs. Make delegates to the
# underlying tools (go, turbo, pnpm, helm, kind, cz, pre-commit) so those tools
# can be swapped without retraining anyone. Toolchain versions are pinned in
# mise.toml. This is a skeleton: targets echo their intent until the underlying
# code lands. Add a target here rather than documenting a raw command.

# Use bash with strict flags for every recipe.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Meta
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Environment
# ---------------------------------------------------------------------------

.PHONY: setup
setup: ## Install toolchain deps, pre-commit hooks, and init git-flow
	@echo ">> setup: installing pinned toolchain (mise)"
	@command -v mise >/dev/null 2>&1 && mise install || echo "   (mise not found; skipping — see mise.toml)"
	@echo ">> setup: installing codegen toolchain (tools/codegen)"
	@command -v pnpm >/dev/null 2>&1 && pnpm --dir tools/codegen install --ignore-workspace || echo "   (pnpm not found; skipping codegen deps)"
	@echo ">> setup: installing workspace deps (pnpm)"
	@command -v pnpm >/dev/null 2>&1 && pnpm install || echo "   (pnpm not found; skipping workspace install)"
	@echo ">> setup: installing pre-commit hooks"
	@command -v pre-commit >/dev/null 2>&1 && pre-commit install --install-hooks --hook-type commit-msg --hook-type pre-commit || echo "   (pre-commit not found; skipping)"
	@echo ">> setup: initializing git-flow (non-interactive, project branch names)"
	@echo "   (git-flow init wiring lands with the bootstrap CLI)"

# ---------------------------------------------------------------------------
# Codegen — contracts are the source of truth (api/ -> generated clients/types)
# ---------------------------------------------------------------------------

.PHONY: generate
generate: ## Regenerate code from api/ contracts (JSON Schema -> Go/TS types, OpenAPI -> Go server)
	@echo ">> generate: api/ -> kernel/pkg/model, kernel gateway server, packages/query-client"
	@bash tools/codegen/generate.sh

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Build kernel, cli, web, and packages
	@echo ">> build: kernel + cli (go)"
	@cd kernel && go build ./... && cd ../cli && go build ./... 2>/dev/null || true
	@echo ">> build: design system + web shell (pnpm)"
	@command -v pnpm >/dev/null 2>&1 && ( \
		pnpm --filter @llmobs/query-client build && \
		pnpm --filter @llmobs/tokens build && \
		pnpm --filter @llmobs/ui build && \
		pnpm --filter @llmobs/plugin-sdk build && \
		NODE_ENV=production pnpm --filter @llmobs/shell build && \
		NODE_ENV=production pnpm --filter @llmobs/plugin-tracing build && \
		pnpm --filter @llmobs/shell check:singletons \
	) || echo "   (pnpm not found; skipping web build)"

# ---------------------------------------------------------------------------
# Test / Lint
# ---------------------------------------------------------------------------

.PHONY: test
test: ## Run unit tests (Go + TS)
	@echo ">> test: kernel go test (incl. spec-parsed merge vectors + fixture conformance)"
	@cd kernel && go test ./...
	@echo ">> test: cli (incl. plugin-create scaffold)"
	@cd cli && go test ./...
	@echo ">> test: (TS) SDK hooks + testing utilities (vitest; build deps first)"
	@pnpm --filter @llmobs/query-client build >/dev/null
	@pnpm --filter @llmobs/tokens build >/dev/null
	@pnpm --filter @llmobs/ui build >/dev/null
	@pnpm --filter @llmobs/schema-form build >/dev/null
	@pnpm --filter @llmobs/plugin-sdk build >/dev/null
	@pnpm --filter @llmobs/plugin-sdk test
	@echo ">> test: (TS) tracing plugin (buildTree guards)"
	@pnpm --filter @llmobs/plugin-tracing test

.PHONY: plugin-python-test
plugin-python-test: ## Python plugin backend tests (cross-language interop + Langfuse/n8n translation)
	@echo ">> plugin-python: Python verifies a Go-signed token vector + Langfuse->OTLP + n8n->OTLP translation"
	@cd plugins/langfuse-compat/backend && python3 interop_test.py && python3 translate_test.py
	@cd plugins/n8n-compat/backend && python3 translate_test.py && python3 poll_test.py

.PHONY: lint
lint: ## Run all linters (Go + TS + boundary/import checks)
	@echo ">> lint: golangci-lint, eslint, prettier, import-boundary checks"

# ---------------------------------------------------------------------------
# Dev / E2E
# ---------------------------------------------------------------------------

.PHONY: dev
dev: ## Dev inner loop: Postgres + kernel + shell + tracing plugin, all hot-reloading
	@bash scripts/dev.sh

.PHONY: e2e
e2e: e2e-lite ## Run end-to-end tests (lite compose profile)

.PHONY: e2e-lite
e2e-lite: ## Lite e2e: up -> emit agent trace -> query spans back through the DSL
	@bash scripts/e2e-lite.sh

e2e-plugin: ## Fully-live plugin e2e: kernel + Python plugin, handshake+token delivery, LANGFUSE_HOST->us migration
	@bash scripts/e2e-plugin.sh

.PHONY: e2e-k8s
e2e-k8s: ## Run Kubernetes e2e (kind + Helm + operator)
	@echo ">> e2e-k8s: kind cluster, helm install, operator reconcile, plugin lifecycle"

# ---------------------------------------------------------------------------
# Conformance / Perf / Airgap  (map to release-blocking CI gates)
# ---------------------------------------------------------------------------

.PHONY: conformance
conformance: ## Replay SDK dialect fixtures through the normalizer
	@echo ">> conformance: replay kernel/testdata/fixtures through the normalizer"
	@cd kernel && go test ./internal/dataplane/normalize/ -run Fixture

.PHONY: perf
perf: ## Run loadgen against the performance targets (D13)
	@echo ">> perf: tools/loadgen against ingest/query targets"

.PHONY: airgap
airgap: ## Build and verify the offline install bundle (D14)
	@echo ">> airgap: build offline bundle, install in network-isolated context"

# ---------------------------------------------------------------------------
# Release  (maintainers only; see .claude/skills/release)
# ---------------------------------------------------------------------------

.PHONY: bump
bump: ## Compute the next train version, update CHANGELOG, and tag (cz bump)
	@echo ">> bump: cz bump (Conventional Commits -> version + CHANGELOG + tag)"

.PHONY: clean
clean: ## Remove build artifacts and caches
	@echo ">> clean: build outputs, turbo cache, go test cache"
