# kagent ACP sandbox image family.
#
# Layout:
#   Stage "builder"  — compiles the acp-shim static binary from this repo.
#   Stage "base"     — minimal runtime + acp-shim entrypoint. Published as
#                      ghcr.io/kagent-dev/kagent/acp-sandbox-base. Contains
#                      NO agent: it is the uniform transport layer.
#   Agent targets    — thin layers that install one stdio ACP agent and set
#                      its launch command. Build with --target <agent>:
#
#       docker build -f docker/acp-sandbox/Dockerfile --target gemini   go/
#       docker build -f docker/acp-sandbox/Dockerfile --target hermes   go/
#       docker build -f docker/acp-sandbox/Dockerfile --target codex    go/
#       docker build -f docker/acp-sandbox/Dockerfile --target openclaw go/
#
# The contract between base and agent layers is intentionally tiny:
#   - ENTRYPOINT is acp-shim; it serves ws://0.0.0.0:9000/acp.
#   - The agent layer provides the child command either as CMD args after
#     "--" or via the ACP_SHIM_CHILD env var.
#   - The bearer token is mounted at /var/run/acp/token by the Substrate
#     workload spec (same pattern as the AgentHarness gatewayToken).
#
# OpenClaw note: NemoClaw's sandbox-base is built from the NemoClaw repo
# for OpenShell, so we don't extend it. The "openclaw" target below installs
# the OpenClaw CLI directly on the kagent base (same npm install NemoClaw's
# Dockerfile.base uses) and runs its own gateway alongside the shim.

### Stage 0: build the acp-shim Go binary
ARG BASE_IMAGE_REGISTRY=cgr.dev
ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM $BASE_IMAGE_REGISTRY/chainguard/go:latest AS builder
ARG TARGETARCH
ARG TARGETOS

WORKDIR /workspace

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod,rw \
    --mount=type=cache,target=/root/.cache/go-build,rw \
    go mod download

COPY api/ api/
COPY core/ core/
COPY adk/ adk/

ARG LDFLAGS
RUN --mount=type=cache,target=/root/go/pkg/mod,rw \
    --mount=type=cache,target=/root/.cache/go-build,rw \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -trimpath -ldflags "$LDFLAGS" -o /acp-shim ./core/cmd/acp-shim

### Stage 1: kagent-owned sandbox base — shim only, no agent.
FROM debian:trixie-slim AS base
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
    && rm -rf /var/lib/apt/lists/*

# Single unprivileged user. Substrate provides the isolation boundary
# (microVM); we don't need OpenShell's gateway/sandbox user split here.
RUN groupadd -r agent && useradd -r -m -g agent -d /home/agent -s /bin/bash agent \
    && mkdir -p /var/run/acp \
    && chown agent:agent /var/run/acp

COPY --from=builder /acp-shim /usr/local/bin/acp-shim

USER agent
WORKDIR /home/agent
EXPOSE 9000

ENV ACP_SHIM_TOKEN_FILE=/var/run/acp/token

ENTRYPOINT ["/usr/local/bin/acp-shim"]
# Agent layers append the child command, e.g.:
#   CMD ["--", "hermes", "acp"]

### Shared: base + Node.js 22 for npm-distributed agents. Debian trixie's
### apt nodejs is v20, below OpenClaw's >=22.19 requirement (NemoClaw's
### sandbox-base uses node:22-trixie-slim for the same reason), so take
### the runtime from the official image.
FROM node:22-trixie-slim AS node
FROM base AS node-base
USER root
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && node --version && npm --version
USER agent

### Agent target: Gemini CLI (npm)
FROM node-base AS gemini
USER root
RUN npm install -g @google/gemini-cli
USER agent
# Gemini CLI keeps session state on disk; per-connection is fine, but
# long-lived (the default) avoids cold starts.
CMD ["--", "gemini", "--experimental-acp"]

### Agent target: Hermes (pip)
FROM base AS hermes
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
USER agent
RUN python3 -m venv /home/agent/.venv \
    && /home/agent/.venv/bin/pip install --no-cache-dir "hermes-agent[acp]"
ENV PATH="/home/agent/.venv/bin:${PATH}"
# Hermes ACP sessions are in-memory: the child MUST be long-lived (default)
# so session/load works across bridge reconnects.
CMD ["--", "hermes", "acp"]

### Agent target: Codex CLI — via Zed's codex-acp adapter, which wraps the
### Codex CLI with a stdio ACP server (the codex CLI itself has no ACP mode).
FROM node-base AS codex
USER root
RUN npm install -g @openai/codex @zed-industries/codex-acp
USER agent
CMD ["--", "codex-acp"]

### Agent target: Claude Code (npm) — via Zed's claude-code-acp adapter,
### which wraps the Claude Code SDK with a stdio ACP server.
FROM node-base AS claude
USER root
RUN npm install -g @zed-industries/claude-code-acp
USER agent
CMD ["--", "claude-code-acp"]

### Agent target: GitHub Copilot CLI (npm) — native ACP server via
### `copilot --acp --stdio` (public preview). Auth: a GitHub token with
### Copilot access, supplied via the harness spec.env (GH_TOKEN).
FROM node-base AS copilot
USER root
RUN npm install -g @github/copilot
USER agent
CMD ["--", "copilot", "--acp", "--stdio"]

### Agent target: Goose (Block) — native ACP server via `goose acp`.
### Single static Rust binary from the GitHub release; the keyring is
### disabled at runtime so provider API keys are read from env vars.
FROM base AS goose
USER root
RUN apt-get update && apt-get install -y --no-install-recommends bzip2 libxcb1 libdbus-1-3 \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && case "$arch" in \
         amd64) triple=x86_64-unknown-linux-gnu ;; \
         arm64) triple=aarch64-unknown-linux-gnu ;; \
         *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/block/goose/releases/download/stable/goose-${triple}.tar.bz2" \
       | tar -xj -C /usr/local/bin ./goose \
    && chmod 755 /usr/local/bin/goose
USER agent
ENV GOOSE_DISABLE_KEYRING=1
CMD ["--", "goose", "acp"]

### Agent target: OpenClaw — built on the kagent base, NOT on NemoClaw's
### sandbox-base (that image is built from the NemoClaw repo and carries
### OpenShell-specific contracts — gateway/sandbox users, gosu, .openclaw
### tree — that we don't want to depend on). We install the OpenClaw CLI
### the same way NemoClaw's Dockerfile.base does: npm install -g.
###
### `openclaw acp` is a stdio→WS bridge that needs a sandbox-local gateway,
### so this target uses a launcher that starts `openclaw gateway` in the
### background before the shim takes over.
FROM node-base AS openclaw
ARG OPENCLAW_VERSION=2026.5.27
USER root
RUN npm install -g "openclaw@${OPENCLAW_VERSION}"
# Gateway helper: start the sandbox-local gateway if it isn't accepting
# connections, and wait for it. Used both at boot and from the ACP child
# wrapper — under Substrate the actor is checkpointed/restored and any
# process started before the snapshot (the gateway, or a supervising
# subshell) cannot be relied on afterwards, so the gateway is re-ensured
# at connection time by a freshly spawned process.
COPY --chmod=755 <<'EOF' /usr/local/bin/openclaw-gateway-ensure
#!/bin/sh
set -u
: "${OPENCLAW_GATEWAY_PORT:=18789}"
probe() { curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${OPENCLAW_GATEWAY_PORT}/"; }
probe && exit 0
nohup openclaw gateway run --port "${OPENCLAW_GATEWAY_PORT}" --allow-unconfigured \
    >> "${HOME}/.openclaw/gateway.log" 2>&1 &
i=0
while [ "$i" -lt 60 ]; do
    probe && exit 0
    i=$((i+1))
    sleep 1
done
echo "openclaw-gateway-ensure: gateway did not come up on :${OPENCLAW_GATEWAY_PORT}" >&2
exit 1
EOF
# ACP child: spawned by the shim per bridge; guarantees a live gateway
# before exec'ing the stdio<->WS bridge.
COPY --chmod=755 <<'EOF' /usr/local/bin/openclaw-acp-child
#!/bin/sh
set -eu
: "${OPENCLAW_GATEWAY_PORT:=18789}"
/usr/local/bin/openclaw-gateway-ensure
exec openclaw acp --url "ws://127.0.0.1:${OPENCLAW_GATEWAY_PORT}"
EOF
COPY --chmod=755 <<'EOF' /usr/local/bin/openclaw-acp-entrypoint
#!/bin/sh
# Write the gateway config (loopback bind, auth mode "none" — gateway and
# ACP client share this container; the externally reachable surface is the
# shim, which enforces its own bearer token), pre-warm the gateway so the
# golden snapshot includes a hot npm/node page cache, then hand off to the
# shim with the gateway-ensuring child wrapper.
set -eu
: "${OPENCLAW_GATEWAY_PORT:=18789}"
mkdir -p "${HOME}/.openclaw"
if [ ! -f "${HOME}/.openclaw/openclaw.json" ]; then
    printf '{"gateway":{"port":%s,"bind":"loopback","auth":{"mode":"none"}}}\n' \
        "${OPENCLAW_GATEWAY_PORT}" > "${HOME}/.openclaw/openclaw.json"
fi
/usr/local/bin/openclaw-gateway-ensure || true
exec /usr/local/bin/acp-shim "$@" -- /usr/local/bin/openclaw-acp-child
EOF
USER agent
ENTRYPOINT ["/usr/local/bin/openclaw-acp-entrypoint"]
CMD []
