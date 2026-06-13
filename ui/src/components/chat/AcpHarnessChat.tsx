"use client";

// ACP harness chat — drives the standard kagent chat experience over the
// Agent Client Protocol for substrate AgentHarness actors, through the
// controller's same-origin WebSocket proxy (/api/agentharnesses/{ns}/{name}/acp).
//
// On connect the ACP handshake runs automatically (initialize → session/new),
// then each chat message is a session/prompt; streaming output arrives as
// session/update notifications (agent_message_chunk, agent_thought_chunk,
// tool_call, tool_call_update, plan) and is mapped onto the same A2A Message
// shapes the rest of the chat UI renders (ChatMessage / ToolCallDisplay /
// StreamingMessage). Agent-initiated session/request_permission requests are
// auto-approved.
//
// When the agent advertises loadSession + sessionCapabilities.list, past
// sessions are fetched via session/list and offered in a "Previous chats"
// picker; selecting one calls session/load, which replays the transcript as
// user_message_chunk / agent_message_chunk updates before the chat resumes.

import type React from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowBigUp, RefreshCw, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { ScrollArea } from "@/components/ui/scroll-area";
import ChatMessage from "@/components/chat/ChatMessage";
import StreamingMessage from "./StreamingMessage";
import StatusDisplay from "./StatusDisplay";
import { createMessage, ProcessedToolCallData, ProcessedToolResultData } from "@/lib/messageHandlers";
import { getStatusPlaceholder } from "@/lib/statusUtils";
import type { ChatStatus } from "@/types";
import type { Message } from "@a2a-js/sdk";
import { v4 as uuidv4 } from "uuid";
import { toast } from "sonner";

type ConnState = "connecting" | "initializing" | "creating-session" | "loading-session" | "ready" | "running" | "disconnected";

type JsonRpcMessage = {
  jsonrpc?: string;
  id?: number | string;
  method?: string;
  params?: Record<string, unknown>;
  result?: Record<string, unknown>;
  error?: { code: number; message: string; data?: unknown };
};

type SessionUpdate = {
  sessionUpdate?: string;
  content?: unknown;
  toolCallId?: string;
  title?: string;
  kind?: string;
  status?: string;
  rawInput?: Record<string, unknown>;
  rawOutput?: unknown;
  entries?: { content?: string; status?: string }[];
};

type PermissionOption = { optionId?: string; name?: string; kind?: string };

/** SessionInfo entries returned by ACP session/list. */
type AcpSessionInfo = { sessionId: string; title?: string; updatedAt?: string };

function chunkText(content: unknown): string {
  if (content && typeof content === "object" && "text" in content) {
    const text = (content as { text?: unknown }).text;
    return typeof text === "string" ? text : "";
  }
  return "";
}

function toolResultText(update: SessionUpdate): string {
  if (Array.isArray(update.content)) {
    const parts = (update.content as { content?: { text?: string } }[])
      .map((c) => c?.content?.text ?? "")
      .filter(Boolean);
    if (parts.length > 0) return parts.join("\n");
  }
  if (update.rawOutput !== undefined) {
    return typeof update.rawOutput === "string" ? update.rawOutput : JSON.stringify(update.rawOutput, null, 2);
  }
  return "";
}

function connToChatStatus(conn: ConnState): ChatStatus {
  switch (conn) {
    case "ready":
      return "ready";
    case "running":
      return "working";
    case "disconnected":
      return "error";
    default:
      return "thinking";
  }
}

interface AcpHarnessChatProps {
  /** Same-origin WebSocket path, e.g. /api/agentharnesses/kagent/my-claw/acp */
  acpPath: string;
  namespace: string;
  agentName: string;
  /** Callback when ACP sessions are updated from session/list. */
  onSessionsUpdate?: (sessions: AcpSessionInfo[]) => void;
  /** The session ID to load on mount (from sidebar click). */
  initialLoadSessionId?: string;
}

export default function AcpHarnessChat({
  acpPath,
  namespace,
  agentName,
  onSessionsUpdate,
  initialLoadSessionId,
}: AcpHarnessChatProps) {
  const [conn, setConn] = useState<ConnState>("connecting");
  const [messages, setMessages] = useState<Message[]>([]);
  const [streamingContent, setStreamingContent] = useState("");
  const [currentInputMessage, setCurrentInputMessage] = useState("");
  const [pastSessions, setPastSessions] = useState<AcpSessionInfo[]>([]);
  const [loadedSessionId, setLoadedSessionId] = useState("");

  const wsRef = useRef<WebSocket | null>(null);
  const nextIdRef = useRef(1);
  const pendingRef = useRef(new Map<number | string, string>());
  const sessionIdRef = useRef<string | null>(null);
  const streamBufRef = useRef("");
  const streamKindRef = useRef<"agent" | "thought" | "user" | null>(null);
  const toolNamesRef = useRef(new Map<string, string>());
  const toolResultsSentRef = useRef(new Set<string>());
  const planMessageIdRef = useRef<string | null>(null);
  const authMethodsRef = useRef<string[]>([]);
  const authTriedRef = useRef(false);
  const authQueueRef = useRef<string[]>([]);
  // session/load: replay in progress (user_message_chunk only rendered then),
  // and the session id we asked to load (the load response carries no id).
  const replayingRef = useRef(false);
  const pendingLoadRef = useRef<string | null>(null);
  const canListSessionsRef = useRef(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  const chatStatus = connToChatStatus(conn);
  const agentContext = { namespace, agentName };

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages, streamingContent, conn]);

  const appendMessage = useCallback((message: Message) => {
    setMessages((prev) => [...prev, message]);
  }, []);

  const flushStream = useCallback(() => {
    const buf = streamBufRef.current;
    const kind = streamKindRef.current;
    streamBufRef.current = "";
    streamKindRef.current = null;
    setStreamingContent("");
    if (buf.trim()) {
      const role = kind === "thought" ? "thinking" : kind === "user" ? "user" : "assistant";
      appendMessage(createMessage(buf, role, { originalType: "TextMessage" }));
    }
  }, [appendMessage]);

  const addStreamChunk = useCallback(
    (kind: "agent" | "thought" | "user", text: string) => {
      if (!text) return;
      if (streamKindRef.current !== null && streamKindRef.current !== kind) {
        flushStream();
      }
      streamKindRef.current = kind;
      streamBufRef.current += text;
      setStreamingContent(streamBufRef.current);
    },
    [flushStream],
  );

  const sendRaw = useCallback((msg: Record<string, unknown>) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(msg));
  }, []);

  const rpc = useCallback(
    (method: string, params: Record<string, unknown>) => {
      const id = nextIdRef.current++;
      pendingRef.current.set(id, method);
      sendRaw({ jsonrpc: "2.0", id, method, params });
    },
    [sendRaw],
  );

  const handleSessionUpdate = useCallback(
    (update: SessionUpdate) => {
      switch (update.sessionUpdate) {
        case "agent_message_chunk":
          addStreamChunk("agent", chunkText(update.content));
          break;
        case "agent_thought_chunk":
          addStreamChunk("thought", chunkText(update.content));
          break;
        case "user_message_chunk":
          // Only rendered during session/load replay; live user messages are
          // already appended locally when the prompt is sent.
          if (replayingRef.current) {
            addStreamChunk("user", chunkText(update.content));
          }
          break;
        case "tool_call": {
          flushStream();
          const id = update.toolCallId || uuidv4();
          const name = update.title || update.kind || "tool";
          toolNamesRef.current.set(id, name);
          const toolCallData: ProcessedToolCallData[] = [{ id, name, args: update.rawInput ?? {} }];
          appendMessage(
            createMessage("", "assistant", {
              originalType: "ToolCallRequestEvent",
              additionalMetadata: { toolCallData },
            }),
          );
          break;
        }
        case "tool_call_update": {
          const id = update.toolCallId;
          if (!id) break;
          const status = update.status ?? "";
          if (status !== "completed" && status !== "failed") break;
          if (toolResultsSentRef.current.has(id)) break;
          toolResultsSentRef.current.add(id);
          const name = toolNamesRef.current.get(id) || update.title || "tool";
          const toolResultData: ProcessedToolResultData[] = [
            {
              call_id: id,
              name,
              content: toolResultText(update) || (status === "failed" ? "tool call failed" : "done"),
              is_error: status === "failed",
            },
          ];
          appendMessage(
            createMessage("", "assistant", {
              originalType: "ToolCallExecutionEvent",
              additionalMetadata: { toolResultData },
            }),
          );
          break;
        }
        case "plan": {
          const text = (update.entries ?? [])
            .map((e) => `${e.status === "completed" ? "✓" : e.status === "in_progress" ? "▸" : "○"} ${e.content ?? ""}`)
            .join("\n");
          if (!text) break;
          if (planMessageIdRef.current === null) {
            planMessageIdRef.current = uuidv4();
          }
          const planId = planMessageIdRef.current;
          const planMessage = createMessage(text, "plan", { messageId: planId, originalType: "TextMessage" });
          setMessages((prev) =>
            prev.some((m) => m.messageId === planId)
              ? prev.map((m) => (m.messageId === planId ? planMessage : m))
              : [...prev, planMessage],
          );
          break;
        }
        default:
          break;
      }
    },
    [addStreamChunk, appendMessage, flushStream],
  );

  const handleAgentRequest = useCallback(
    (msg: JsonRpcMessage) => {
      if (msg.method === "session/request_permission") {
        const params = msg.params as { options?: PermissionOption[]; toolCall?: { title?: string } } | undefined;
        const options = params?.options ?? [];
        const allow =
          options.find((o) => o.kind === "allow_once") ??
          options.find((o) => o.kind === "allow_always") ??
          options[0];
        if (allow?.optionId) {
          sendRaw({
            jsonrpc: "2.0",
            id: msg.id,
            result: { outcome: { outcome: "selected", optionId: allow.optionId } },
          });
        } else {
          sendRaw({ jsonrpc: "2.0", id: msg.id, result: { outcome: { outcome: "cancelled" } } });
        }
        return;
      }
      // fs/* and anything else: we advertised no client capabilities.
      sendRaw({
        jsonrpc: "2.0",
        id: msg.id,
        error: { code: -32601, message: `method not supported by this client: ${msg.method}` },
      });
    },
    [sendRaw],
  );

  const handleMessage = useCallback(
    (msg: JsonRpcMessage) => {
      // Response to one of our requests.
      if (msg.id !== undefined && msg.method === undefined) {
        const method = pendingRef.current.get(msg.id);
        pendingRef.current.delete(msg.id);
        if (msg.error) {
          // Some agents (e.g. codex-acp) refuse session/new until an explicit
          // authenticate call, even when credentials are already present as
          // env vars. Try each advertised auth method (API-key ones first)
          // until one succeeds.
          if (method === "session/new" && !authTriedRef.current && authMethodsRef.current.length > 0) {
            authTriedRef.current = true;
            const ids = authMethodsRef.current;
            authQueueRef.current = [
              ...ids.filter((id) => id.includes("api-key")),
              ...ids.filter((id) => !id.includes("api-key")),
            ];
            rpc("authenticate", { methodId: authQueueRef.current.shift() });
            return;
          }
          // An auth method can fail (e.g. its env var is unset); fall through
          // to the next one before giving up.
          if (method === "authenticate" && authQueueRef.current.length > 0) {
            rpc("authenticate", { methodId: authQueueRef.current.shift() });
            return;
          }
          // Listing sessions is best-effort; degrade silently to no picker.
          if (method === "session/list") {
            console.debug("acp: session/list failed", msg.error.message);
            return;
          }
          toast.error(`${method ?? "request"} failed: ${msg.error.message}`);
          if (method === "session/prompt") {
            flushStream();
            setConn("ready");
          } else if (method === "session/load") {
            replayingRef.current = false;
            pendingLoadRef.current = null;
            flushStream();
            // Keep the previous session usable; only the load failed.
            setConn("ready");
          } else if (method === "initialize" || method === "session/new" || method === "authenticate") {
            wsRef.current?.close();
          }
          return;
        }
        if (method === "initialize") {
          const methods = (msg.result?.authMethods as { id?: string }[] | undefined) ?? [];
          authMethodsRef.current = methods.map((m) => m.id).filter((id): id is string => typeof id === "string");
          // Previous chats: remember whether the agent supports session
          // listing + loading; the list itself is fetched after session/new
          // succeeds (codex requires authenticate before session/list).
          const caps = (msg.result?.agentCapabilities ?? {}) as {
            loadSession?: boolean;
            sessionCapabilities?: { list?: unknown };
          };
          canListSessionsRef.current = caps.loadSession === true && caps.sessionCapabilities?.list !== undefined;
          setConn("creating-session");
          rpc("session/new", { cwd: "/home/agent", mcpServers: [] });
        } else if (method === "authenticate") {
          authQueueRef.current = [];
          rpc("session/new", { cwd: "/home/agent", mcpServers: [] });
        } else if (method === "session/list") {
          const sessions = (msg.result?.sessions as AcpSessionInfo[] | undefined) ?? [];
          const sorted = sessions
            .filter((s) => typeof s.sessionId === "string" && s.sessionId.length > 0)
            .sort((a, b) => (b.updatedAt ?? "").localeCompare(a.updatedAt ?? ""));
          setPastSessions(sorted);
          onSessionsUpdate?.(sorted);
          // Emit event for sidebar to pick up
          if (typeof window !== "undefined") {
            window.dispatchEvent(
              new CustomEvent("acp-sessions-updated", {
                detail: { agentRef: `${namespace}/${agentName}`, sessions: sorted },
              })
            );
          }
        } else if (method === "session/load") {
          replayingRef.current = false;
          flushStream();
          if (pendingLoadRef.current) {
            sessionIdRef.current = pendingLoadRef.current;
            setLoadedSessionId(pendingLoadRef.current);
          }
          pendingLoadRef.current = null;
          setConn("ready");
        } else if (method === "session/new") {
          const sid = msg.result?.sessionId as string | undefined;
          if (!sid) {
            toast.error("session/new returned no sessionId");
            wsRef.current?.close();
            return;
          }
          sessionIdRef.current = sid;
          setConn("ready");
          // Fetch previous chats now that any required authenticate has run.
          if (canListSessionsRef.current) {
            rpc("session/list", {});
          }
        } else if (method === "session/prompt") {
          const stop = msg.result?.stopReason as string | undefined;
          flushStream();
          planMessageIdRef.current = null;
          setConn("ready");
          if (stop && stop !== "end_turn") {
            toast.info(`Turn ended: ${stop}`);
          }
          // Pick up the current session and freshly generated titles.
          if (canListSessionsRef.current) {
            rpc("session/list", {});
          }
        }
        return;
      }
      // Agent-initiated request.
      if (msg.id !== undefined && msg.method !== undefined) {
        handleAgentRequest(msg);
        return;
      }
      // Notification.
      if (msg.method === "session/update") {
        const update = (msg.params as { update?: SessionUpdate } | undefined)?.update;
        if (update) handleSessionUpdate(update);
      }
    },
    [flushStream, handleAgentRequest, handleSessionUpdate, rpc],
  );

  const connect = useCallback(() => {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${window.location.host}${acpPath}`;
    setConn("connecting");
    const ws = new WebSocket(url);
    wsRef.current = ws;
    ws.onopen = () => {
      authMethodsRef.current = [];
      authTriedRef.current = false;
      authQueueRef.current = [];
      replayingRef.current = false;
      pendingLoadRef.current = null;
      setPastSessions([]);
      setLoadedSessionId("");
      setConn("initializing");
      rpc("initialize", {
        protocolVersion: 1,
        clientCapabilities: { fs: { readTextFile: false, writeTextFile: false } },
      });
    };
    ws.onmessage = (ev) => {
      try {
        handleMessage(JSON.parse(String(ev.data)) as JsonRpcMessage);
      } catch {
        console.debug("acp: received non-JSON frame", ev.data);
      }
    };
    ws.onclose = (ev) => {
      flushStream();
      setConn("disconnected");
      sessionIdRef.current = null;
      pendingRef.current.clear();
      if (ev.code !== 1000 && ev.code !== 1001) {
        toast.error("Disconnected from the agent. The actor may still be starting — try reconnecting.");
      }
    };
  }, [acpPath, flushStream, handleMessage, rpc]);

  // Auto-connect on mount; close the socket on unmount.
  useEffect(() => {
    connect();
    return () => wsRef.current?.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [acpPath]);

  // If a session ID is passed (e.g., from sidebar click), load it once ready.
  useEffect(() => {
    if (initialLoadSessionId && conn === "ready") {
      handleLoadSession(initialLoadSessionId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialLoadSessionId, conn]);

  const handleLoadSession = (sid: string) => {
    if (conn !== "ready" || !sid || sid === sessionIdRef.current) return;
    // Clear the transcript; the agent replays the conversation as
    // session/update notifications before answering session/load.
    flushStream();
    setMessages([]);
    setLoadedSessionId(sid);
    toolNamesRef.current.clear();
    toolResultsSentRef.current.clear();
    planMessageIdRef.current = null;
    replayingRef.current = true;
    pendingLoadRef.current = sid;
    setConn("loading-session");
    rpc("session/load", { sessionId: sid, cwd: "/home/agent", mcpServers: [] });
  };

  const handleSendMessage = (e: React.FormEvent) => {
    e.preventDefault();
    const text = currentInputMessage.trim();
    const sid = sessionIdRef.current;
    if (!text || !sid || conn !== "ready") return;
    appendMessage(createMessage(text, "user", { originalType: "TextMessage" }));
    setCurrentInputMessage("");
    toolResultsSentRef.current.clear();
    setConn("running");
    rpc("session/prompt", { sessionId: sid, prompt: [{ type: "text", text }] });
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      e.currentTarget.form?.requestSubmit();
    }
  };

  const handleCancel = () => {
    const sid = sessionIdRef.current;
    if (!sid) return;
    sendRaw({ jsonrpc: "2.0", method: "session/cancel", params: { sessionId: sid } });
  };

  const connectingHint =
    conn === "connecting" || conn === "initializing" || conn === "creating-session" || conn === "loading-session";

  return (
    <div className="w-full h-screen flex flex-col justify-center min-w-full items-center transition-all duration-300 ease-in-out">
      <div className="flex-1 w-full overflow-hidden relative">
        <ScrollArea className="w-full h-full py-12">
          <div className="flex flex-col space-y-5 px-4">
            {messages.length === 0 && !streamingContent ? (
              <div className="flex items-center justify-center h-full min-h-[50vh]">
                <div className="max-w-md rounded-lg border bg-card p-6 text-center shadow-sm">
                  <h2 className="mb-2 text-lg font-medium">
                    {connectingHint ? "Connecting to the agent…" : conn === "disconnected" ? "Disconnected" : "Start a conversation"}
                  </h2>
                  <p className="text-muted-foreground">
                    {connectingHint
                      ? "The first connection can take up to a minute while the sandbox resumes."
                      : conn === "disconnected"
                        ? "Use Reconnect below to start a new session."
                        : "To begin chatting with the agent, type your message in the input box below."}
                  </p>
                </div>
              </div>
            ) : (
              <>
                {messages.map((message, index) => (
                  <ChatMessage
                    key={message.messageId ?? `acp-${index}`}
                    message={message}
                    allMessages={messages}
                    agentContext={agentContext}
                  />
                ))}
                {streamingContent && <StreamingMessage content={streamingContent} />}
              </>
            )}
            <div ref={bottomRef} />
          </div>
        </ScrollArea>
      </div>

      <div className="w-full sticky bg-secondary bottom-0 md:bottom-2 rounded-none md:rounded-lg p-4 border overflow-hidden transition-all duration-300 ease-in-out">
        <div className="flex items-center justify-between mb-4 gap-2">
          <StatusDisplay chatStatus={chatStatus} />
          <div className="flex items-center gap-2">
            {conn === "disconnected" && (
              <Button type="button" size="sm" variant="outline" onClick={connect}>
                <RefreshCw className="h-4 w-4 mr-2" /> Reconnect
              </Button>
            )}
          </div>
        </div>

        <form onSubmit={handleSendMessage}>
          <Textarea
            value={currentInputMessage}
            onChange={(e) => setCurrentInputMessage(e.target.value)}
            placeholder={getStatusPlaceholder(chatStatus)}
            onKeyDown={handleKeyDown}
            className={`min-h-[100px] border-0 shadow-none p-0 focus-visible:ring-0 resize-none ${chatStatus !== "ready" ? "opacity-50 cursor-not-allowed" : ""}`}
            disabled={chatStatus !== "ready"}
          />

          <div className="flex items-center justify-end gap-2 mt-4">
            <Button type="submit" disabled={!currentInputMessage.trim() || chatStatus !== "ready"}>
              Send
              <ArrowBigUp className="h-4 w-4 ml-2" />
            </Button>
            {conn === "running" && (
              <Button type="button" variant="outline" onClick={handleCancel}>
                <X className="h-4 w-4 mr-2" /> Cancel
              </Button>
            )}
          </div>
        </form>
      </div>
    </div>
  );
}
