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
	@echo ">> setup: installing pre-commit hooks"
	@command -v pre-commit >/dev/null 2>&1 && pre-commit install --install-hooks --hook-type commit-msg --hook-type pre-commit || echo "   (pre-commit not found; skipping)"
	@echo ">> setup: initializing git-flow (non-interactive, project branch names)"
	@echo "   (git-flow init wiring lands with the bootstrap CLI)"

# ---------------------------------------------------------------------------
# Codegen — contracts are the source of truth (api/ -> generated clients/types)
# ---------------------------------------------------------------------------

.PHONY: generate
generate: ## Regenerate code from api/ contracts (openapi -> Go/TS, schema -> types)
	@echo ">> generate: api/ -> pkg/model, packages/query-client, schema types"
	@echo "   (delegates to tools/codegen)"

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Build kernel, cli, web, and packages
	@echo ">> build: kernel + cli (go), web + packages (turbo)"

# ---------------------------------------------------------------------------
# Test / Lint
# ---------------------------------------------------------------------------

.PHONY: test
test: ## Run unit tests (Go + TS)
	@echo ">> test: go test ./... ; turbo run test"

.PHONY: lint
lint: ## Run all linters (Go + TS + boundary/import checks)
	@echo ">> lint: golangci-lint, eslint, prettier, import-boundary checks"

# ---------------------------------------------------------------------------
# Dev / E2E
# ---------------------------------------------------------------------------

.PHONY: dev
dev: ## Run the lite profile locally (compose up + web shell)
	@echo ">> dev: docker compose (lite profile) + web shell"

.PHONY: e2e
e2e: ## Run end-to-end tests (lite compose profile)
	@echo ">> e2e: compose up, loadgen smoke, trace visible end-to-end"

.PHONY: e2e-k8s
e2e-k8s: ## Run Kubernetes e2e (kind + Helm + operator)
	@echo ">> e2e-k8s: kind cluster, helm install, operator reconcile, plugin lifecycle"

# ---------------------------------------------------------------------------
# Conformance / Perf / Airgap  (map to release-blocking CI gates)
# ---------------------------------------------------------------------------

.PHONY: conformance
conformance: ## Replay SDK dialect fixtures through the pipeline
	@echo ">> conformance: tools/conformance replays kernel/testdata/fixtures"

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
