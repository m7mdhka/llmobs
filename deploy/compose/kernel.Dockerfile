# Multi-stage build of the kernel daemon (llmobsd) plus the web shell, for the
# lite profile. The kernel serves the built shell as a static SPA, so the whole
# UI+API is one container reachable at :8080.

# ---- Stage 1: build the web shell (Module Federation host) ----
FROM node:22-slim AS web
ENV PNPM_HOME=/pnpm
ENV PATH="/pnpm:$PATH"
RUN corepack enable
WORKDIR /app
# Workspace manifests first (better layer caching for installs).
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY packages/ ./packages/
COPY web/ ./web/
COPY plugins/ ./plugins/
RUN pnpm install --frozen-lockfile --filter @llmobs/shell... --filter @llmobs/plugin-tracing...
# Build shared deps, the shell, and the first-party tracing plugin (a MF remote).
# Fail the build if the host's shared singletons regress.
RUN pnpm --filter @llmobs/query-client build \
 && pnpm --filter @llmobs/tokens build \
 && pnpm --filter @llmobs/ui build \
 && pnpm --filter @llmobs/plugin-sdk build \
 && NODE_ENV=production pnpm --filter @llmobs/shell build \
 && pnpm --filter @llmobs/shell check:singletons \
 && NODE_ENV=production pnpm --filter @llmobs/plugin-tracing build

# ---- Stage 2: build the kernel ----
FROM golang:1.24 AS build
WORKDIR /src
COPY kernel/ ./kernel/
WORKDIR /src/kernel
RUN CGO_ENABLED=0 go build -trimpath -o /out/llmobsd ./cmd/llmobsd

# ---- Stage 3: runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/llmobsd /llmobsd
COPY --from=web /app/web/shell/dist /webui
# First-party tracing plugin, laid out as the dev-mode registry expects:
# <plugin-dir>/<name>/{llmobs-plugin.yaml, dist/}.
COPY --from=web /app/plugins/tracing/llmobs-plugin.yaml /plugins/tracing/llmobs-plugin.yaml
COPY --from=web /app/plugins/tracing/frontend/dist /plugins/tracing/dist
ENV LLMOBS_WEBUI_DIR=/webui
ENV LLMOBS_PLUGIN_DIR=/plugins
EXPOSE 4317 4318 8080
ENTRYPOINT ["/llmobsd"]
