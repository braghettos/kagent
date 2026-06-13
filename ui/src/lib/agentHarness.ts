import type { AgentResponse } from "@/types";
import { isHarnessListRow, isOpenshellSandboxRow, isSubstrateHarnessRow } from "@/lib/openshellSandboxAgents";

/**
 * Sandbox CR backends that identify an **agent harness** (declarative harness UX: channels, harness create flow, etc.)
 * as opposed to a generic OpenShell/SSH sandbox row.
 *
 * Extend this union when new harness runtimes are added; pair with UI/server handling for each backend.
 */
export const AGENT_HARNESS_BACKENDS = [
  "openclaw",
  "nemoclaw",
  "hermes",
  "codex",
  "claude",
  "copilot",
  "gemini",
  "goose",
] as const;

export type AgentHarnessBackend = (typeof AGENT_HARNESS_BACKENDS)[number];

export function isAgentHarnessBackend(value: string | undefined | null): value is AgentHarnessBackend {
  return AGENT_HARNESS_BACKENDS.some((b) => b === value);
}

export function getAgentHarnessRuntime(item: AgentResponse): "openshell" | "substrate" | undefined {
  if (!isHarnessListRow(item)) {
    return undefined;
  }
  if (isSubstrateHarnessRow(item)) {
    return "substrate";
  }
  return "openshell";
}

/**
 * When this agent row represents an OpenClaw/NemoClaw harness, returns spec.backend.
 * Other AgentHarness backends (e.g. openshell-only rows) are not classified here.
 */
export function getAgentHarnessBackend(item: AgentResponse): AgentHarnessBackend | undefined {
  if (!isHarnessListRow(item)) {
    return undefined;
  }
  const backend =
    item.substrateAgentHarness?.backend ?? item.openshellAgentHarness?.backend;
  return isAgentHarnessBackend(backend) ? backend : undefined;
}

/** True when the agents-list row is an agent harness. */
export function isAgentHarness(item: AgentResponse): boolean {
  return getAgentHarnessBackend(item) !== undefined;
}

/**
 * Default interactive command when opening the OpenShell terminal for a harness backend.
 * Keep in sync with Go: openclaw.DefaultSSHLaunchCommand / hermes.DefaultSSHLaunchCommand.
 */
export function defaultHarnessSSHLaunchCommand(backend: AgentHarnessBackend): string {
  switch (backend) {
    case "hermes":
      return "cd /sandbox/.hermes && exec hermes";
    case "codex":
      return "codex";
    case "claude":
      return "claude";
    case "copilot":
      return "copilot";
    case "gemini":
      return "gemini";
    case "goose":
      return "goose";
    case "openclaw":
    case "nemoclaw":
      return "openclaw tui";
    default: {
      const _exhaustive: never = backend;
      return _exhaustive;
    }
  }
}

/** Emoji shown beside harness agents in list/card views. */
export function agentHarnessIcon(backend: AgentHarnessBackend): string {
  switch (backend) {
    case "hermes":
      return "☤";
    case "codex":
      return "⧗";
    case "claude":
      return "✻";
    case "copilot":
      return "⦿";
    case "gemini":
      return "✦";
    case "goose":
      return "🪿";
    case "openclaw":
    case "nemoclaw":
      return "🦞";
    default: {
      const _exhaustive: never = backend;
      return _exhaustive;
    }
  }
}

/** Short label for the agent list “type” column; harness-specific where known. */
export function agentHarnessTypeLabel(backend: AgentHarnessBackend): string {
  switch (backend) {
    case "openclaw":
      return "OpenClaw";
    case "nemoclaw":
      return "NemoClaw";
    case "hermes":
      return "Hermes";
    case "codex":
      return "Codex";
    case "claude":
      return "Claude Code";
    case "copilot":
      return "GitHub Copilot";
    case "gemini":
      return "Gemini CLI";
    case "goose":
      return "Goose";
    default: {
      const _exhaustive: never = backend;
      return _exhaustive;
    }
  }
}

export function agentHarnessRuntimeLabel(runtime: "openshell" | "substrate"): string {
  return runtime === "substrate" ? "Substrate" : "OpenShell";
}
