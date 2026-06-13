"use client";
import { useMemo, useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import ChatGroup from "./SessionGroup";
import type { Session } from "@/types";
import { isToday, isYesterday } from "date-fns";
import { EmptyState } from "./EmptyState";
import { deleteSession, getSessionTasks, createSession } from "@/app/actions/sessions";
import { formatA2AClientError } from "@/lib/a2aErrors";
import type { SandboxChatMode } from "@/lib/sandboxAgentForm";
import { Button } from "@/components/ui/button";
import { PlusCircle } from "lucide-react";
import { toast } from "sonner";

interface GroupedChatsProps {
  agentName: string;
  agentNamespace: string;
  sessions: Session[];
  acpSessions?: Array<{ sessionId: string; title?: string; updatedAt?: string }>;
  onAcpSessionClick?: (sessionId: string) => void;
  chatMode?: SandboxChatMode;
}

function acpSessionLabel(s: { sessionId: string; title?: string; updatedAt?: string }): string {
  const title = s.title?.trim() || s.sessionId.slice(0, 18);
  if (!s.updatedAt) return title;
  const d = new Date(s.updatedAt);
  return Number.isNaN(d.getTime()) ? title : `${title} · ${d.toLocaleString()}`;
}

export default function GroupedChats({
  agentName,
  agentNamespace,
  sessions,
  acpSessions = [],
  onAcpSessionClick,
  chatMode = "default",
}: GroupedChatsProps) {
  const router = useRouter();
  const hideNewChat = chatMode === "single-session";
  const hideSessionDelete = chatMode === "single-session";
  const provisionSessionOnNewChat = chatMode === "multi-session";

  // Local state to manage sessions for immediate UI updates
  const [localSessions, setLocalSessions] = useState<Session[]>(sessions);

  // Update local sessions when the prop changes
  useEffect(() => {
    setLocalSessions(sessions);
  }, [sessions]);

  const groupedChats = useMemo(() => {
    type SessionWithActivity = {
      session: Session;
      activityTimestamp: number;
    };

    const groups: {
      today: SessionWithActivity[];
      yesterday: SessionWithActivity[];
      older: SessionWithActivity[];
    } = {
      today: [],
      yesterday: [],
      older: [],
    };

    const sessionsWithActivity = localSessions.map(session => ({
      session,
      activityTimestamp: Date.parse(session.updated_at || session.created_at),
    }));

    // Process each session and group by last activity date
    sessionsWithActivity.forEach(sessionWithActivity => {
      const date = new Date(sessionWithActivity.activityTimestamp);
      if (isToday(date)) {
        groups.today.push(sessionWithActivity);
      } else if (isYesterday(date)) {
        groups.yesterday.push(sessionWithActivity);
      } else {
        groups.older.push(sessionWithActivity);
      }
    });

    const sortChats = (sessions: SessionWithActivity[]) =>
      sessions
        .sort((a, b) => b.activityTimestamp - a.activityTimestamp)
        .map(({ session }) => session);

    return {
      today: sortChats(groups.today),
      yesterday: sortChats(groups.yesterday),
      older: sortChats(groups.older),
    };
  }, [localSessions]);

  const onDeleteClick = async (sessionId: string) => {
    try {
      // Immediately remove from local state
      setLocalSessions(prev => prev.filter(session => session.id !== sessionId));

      // Then delete from server
      await deleteSession(sessionId);
    } catch (error) {
      console.error("Error deleting session:", error);
      // If there's an error, restore the session in the UI
      setLocalSessions(sessions);
    }
  };

  const onDownloadClick = async (sessionId: string) => {
    toast.promise(
      getSessionTasks(String(sessionId)).then(messages => {
        const blob = new Blob([JSON.stringify(messages, null, 2)], { type: "application/json" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `session-${sessionId}.json`;
        a.click();
        URL.revokeObjectURL(url);
        return messages;
      }),
      {
        loading: "Downloading session...",
        success: "Session downloaded successfully",
        error: "Failed to download session",
      }
    );
  }

  const handleNewChat = async () => {
    if (provisionSessionOnNewChat) {
      try {
        const created = await createSession({
          agent_ref: `${agentNamespace}/${agentName}`,
        });
        if (created.error || !created.data) {
          toast.error(formatA2AClientError(created.error ?? "Failed to create session"));
          return;
        }
        const agentRef = `${agentNamespace}/${agentName}`;
        window.dispatchEvent(
          new CustomEvent("new-session-created", {
            detail: { agentRef, session: created.data },
          })
        );
        window.location.href = `/agents/${agentNamespace}/${agentName}/chat/${created.data.id}`;
        return;
      } catch (error) {
        console.error("Error creating session:", error);
        toast.error(formatA2AClientError(error instanceof Error ? error.message : "Failed to create session"));
        return;
      }
    }
    // Force a full page reload instead of client-side navigation
    window.location.href = `/agents/${agentNamespace}/${agentName}/chat`;
  };

  const hasNoSessions = !groupedChats.today.length && !groupedChats.yesterday.length && !groupedChats.older.length && acpSessions.length === 0;

  const handleAcpSessionClick = (sessionId: string) => {
    // Navigate with sessionId as query param for ACP harness chat
    router.push(`/agents/${agentNamespace}/${agentName}/chat?sessionId=${encodeURIComponent(sessionId)}`);
    onAcpSessionClick?.(sessionId);
  };

  return (
    <>
      {!hideNewChat && (
      <div className="mb-4 px-2">
        <Button
          variant="secondary"
          className="w-full flex items-center justify-center gap-2"
          onClick={handleNewChat}
        >
          <PlusCircle size={16} />
          New Chat
        </Button>
      </div>
      )}

      {acpSessions.length > 0 && (
        <div className="mb-4 px-2">
          <div className="text-xs font-semibold text-muted-foreground mb-2 px-2">Previous chats</div>
          {acpSessions.map((session) => (
            <button
              key={session.sessionId}
              onClick={() => handleAcpSessionClick(session.sessionId)}
              className="w-full text-left px-2 py-2 text-sm rounded-md hover:bg-accent hover:text-accent-foreground truncate transition-colors"
              title={acpSessionLabel(session)}
            >
              {acpSessionLabel(session)}
            </button>
          ))}
        </div>
      )}

      {hasNoSessions || (localSessions.length === 0 && acpSessions.length === 0) ? (
        <EmptyState variant={hideNewChat ? "singleChat" : "default"} />
      ) : (
        <>
          {groupedChats.today.length > 0 && <ChatGroup title="Today" sessions={groupedChats.today} agentName={agentName} agentNamespace={agentNamespace} onDeleteSession={(sessionId) => onDeleteClick(sessionId)} onDownloadSession={(sessionId) => onDownloadClick(sessionId)} hideSessionDelete={hideSessionDelete} />}
          {groupedChats.yesterday.length > 0 && (
            <ChatGroup title="Yesterday" sessions={groupedChats.yesterday} agentName={agentName} agentNamespace={agentNamespace} onDeleteSession={(sessionId) => onDeleteClick(sessionId)} onDownloadSession={(sessionId) => onDownloadClick(sessionId)} hideSessionDelete={hideSessionDelete} />
          )}
          {groupedChats.older.length > 0 && <ChatGroup title="Older" sessions={groupedChats.older} agentName={agentName} agentNamespace={agentNamespace} onDeleteSession={(sessionId) => onDeleteClick(sessionId)} onDownloadSession={(sessionId) => onDownloadClick(sessionId)} hideSessionDelete={hideSessionDelete} />}
        </>
      )}
    </>
  );
}
