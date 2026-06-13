# ACP sandbox images

Prototype image family for kagent's ACP integration (see
[design/EP-XXXX-acp-integration.md](../../design/EP-XXXX-acp-integration.md)).
Every image in this family runs the same entrypoint — `acp-shim`
([go/core/cmd/acp-shim](../../go/core/cmd/acp-shim)) — which exposes a stdio
ACP agent over `ws://0.0.0.0:9000/acp`, reachable through Substrate's atenet
ingress (WebSocket upgrades are enabled there).

## Why a kagent-owned base instead of extending NemoClaw's sandbox-base

`ghcr.io/kagent-dev/nemoclaw/sandbox-base` was built for **OpenShell**: its
gateway/sandbox user split, gosu privilege separation, and `.openclaw`
directory tree are a stable contract with OpenShell, not with Substrate.
Substrate sandboxes get isolation from the microVM boundary, so that
machinery is dead weight for non-OpenClaw agents — and adopting the image
couples every agent we add to NemoClaw's release cadence.

So the strategy is **one kagent-owned base for everything**:

| Agent | Base image | Why |
|---|---|---|
| Hermes, Codex, Gemini CLI (and future stdio ACP agents) | `acp-sandbox-base` (the `base` stage here) | Minimal, kagent-owned, agent installed in a thin layer |
| OpenClaw | `acp-sandbox-base` too (the `openclaw` stage) | NemoClaw's sandbox-base is built from the NemoClaw repo for OpenShell, so we install the OpenClaw CLI ourselves (`npm install -g openclaw@<ver>`, same as NemoClaw's Dockerfile.base) and run `openclaw gateway` + shim via a small launcher |

The base↔agent contract is three lines: `ENTRYPOINT` is the shim, the agent
layer sets the child command via `CMD ["--", ...]` (or `ACP_SHIM_CHILD`),
and the bearer token is mounted at `/var/run/acp/token`.

## Building

From the repo root:

```sh
docker build -f docker/acp-sandbox/Dockerfile --target gemini -t kagent/acp-sandbox-gemini go/
docker build -f docker/acp-sandbox/Dockerfile --target hermes -t kagent/acp-sandbox-hermes go/
docker build -f docker/acp-sandbox/Dockerfile --target openclaw -t kagent/acp-sandbox-openclaw go/
```

## Trying it in kagent (kind)

The substrate AgentHarness path uses this image by default: creating an
AgentHarness with backend `openclaw` and runtime `substrate` generates an
ActorTemplate whose startup script execs the acp-shim, and the kagent UI
chats with it over `/api/agentharnesses/{ns}/{name}/acp` (the controller
proxies the WebSocket to the actor and injects the gateway token).

For a standalone test without the controller, run it as a plain Deployment:

```sh
# 1. Build and load the image
docker build -f docker/acp-sandbox/Dockerfile --target openclaw -t acp-sandbox-openclaw:dev go/
kind load docker-image acp-sandbox-openclaw:dev --name kagent

# 2. Deploy (Secret token + Deployment + Service in namespace kagent)
kubectl apply -f docker/acp-sandbox/test-deployment.yaml

# 3. Reach the shim
kubectl -n kagent port-forward svc/acp-shim-test 9000:9000
```

Then speak newline-delimited JSON-RPC over WS from a terminal:

```sh
websocat "ws://localhost:9000/acp?access_token=dev-token"
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}
```

## Smoke test (no cluster needed)

```sh
echo -n s3cret > /tmp/token
docker run --rm -p 9000:9000 -v /tmp/token:/var/run/acp/token kagent/acp-sandbox-gemini
# then from another shell, speak newline-delimited JSON-RPC over WS:
websocat -H "Authorization: Bearer s3cret" ws://localhost:9000/acp
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}
```

## Open items (tracked in the EP)

- OpenClaw target: gateway flags/config (`openclaw gateway` invocation in the
  launcher is a best-guess prototype — needs the real port/auth wiring) and
  whether any `.openclaw` directory scaffolding from NemoClaw's base is
  actually required by the CLI at runtime.
- Agent credential injection (`~/.codex/auth.json`, `~/.hermes/.env`, ...)
  belongs to the harness bootstrap, not these images.
- Whether the shim is baked (this approach) or injected via init container
  + shared volume.
