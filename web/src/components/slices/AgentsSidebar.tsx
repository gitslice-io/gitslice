import type { AgentDaemon, Conversation } from "../../api/types";
import { cn } from "../../lib/cn";
import { daemonName } from "../../lib/daemons";
import { Timestamp } from "../Timestamp";
import { ConversationStatusDot } from "./ConversationParts";

export function AgentsSidebar({
  conversations,
  hasSelectedConversation,
  isCreating,
  isSidebarOpen,
  onOpenCreate,
  onSelectConversation,
  onlineDaemons,
  selectedConversationId,
  sliceKey
}: {
  conversations: Conversation[];
  hasSelectedConversation: boolean;
  isCreating: boolean;
  isSidebarOpen: boolean;
  onOpenCreate: () => void;
  onSelectConversation: (id: string) => void;
  onlineDaemons: AgentDaemon[];
  selectedConversationId: string;
  sliceKey: string;
}) {
  const sidebarVisibilityClass = hasSelectedConversation
    ? isSidebarOpen
      ? "hidden lg:block"
      : "hidden"
    : isSidebarOpen
      ? "block"
      : "block lg:hidden";

  return (
    <aside
      aria-label="Agent conversations"
      className={cn(
        "min-h-[60vh] overflow-y-auto p-4",
        sidebarVisibilityClass,
        // On desktop the list becomes a sticky column so it stays in view
        // while the transcript scrolls the page, instead of scrolling off
        // into an empty gutter. self-start keeps the grid item from
        // stretching so sticky has room to resolve.
        "lg:sticky lg:top-16 lg:max-h-[calc(100dvh-5rem)] lg:self-start lg:border-r lg:border-slate-200 dark:lg:border-zinc-800 lg:p-5"
      )}
      id="agent-conversation-sidebar"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
            Conversations
          </h2>
          <p className="mt-1 truncate text-xs leading-5 text-slate-500 dark:text-zinc-400">
            {sliceKey}
          </p>
        </div>
        <button
          className="shrink-0 rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-1.5 text-xs font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-zinc-600"
          disabled={!onlineDaemons.length || isCreating}
          onClick={onOpenCreate}
          type="button"
        >
          New conversation
        </button>
      </div>

      <div className="mt-5">
        <h3 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          Daemons
        </h3>
        {onlineDaemons.length === 0 ? (
          <div className="mt-3 rounded-md border border-dashed border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 p-4 text-sm text-slate-700 dark:text-zinc-300">
            <p className="font-semibold text-zinc-950 dark:text-zinc-50">No online daemons</p>
            <p className="mt-2 leading-6">
              Run{" "}
              <code className="rounded bg-white dark:bg-zinc-900 px-1.5 py-0.5 font-mono text-xs text-slate-800 dark:text-zinc-200">
                gs agent start
              </code>{" "}
              in an empty directory to make an agent available here.
            </p>
          </div>
        ) : (
          <div className="mt-3 grid grid-cols-1 gap-2">
            {onlineDaemons.map((daemon) => (
              <DaemonRow daemon={daemon} key={daemon.id ?? daemon.name} />
            ))}
          </div>
        )}
      </div>

      <div className="mt-5 border-t border-slate-200 dark:border-zinc-800 pt-4">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            History
          </h3>
          {conversations.length ? (
            <span className="font-mono text-xs text-slate-400 dark:text-zinc-500">
              {conversations.length}
            </span>
          ) : null}
        </div>
        {conversations.length === 0 ? (
          <p className="mt-3 rounded-md border border-dashed border-slate-300 dark:border-zinc-700 p-3 text-sm text-slate-600 dark:text-zinc-400">
            No conversations yet.
          </p>
        ) : (
          <ul className="mt-3 grid grid-cols-1 gap-1.5">
            {conversations.map((conversation) => {
              const isSelected = conversation.id === selectedConversationId;
              return (
                <li key={conversation.id}>
                  <button
                    aria-current={isSelected ? "true" : undefined}
                    className={cn(
                      "min-w-0 w-full rounded-md px-3 py-2 text-left text-sm transition active:scale-[0.98]",
                      isSelected
                        ? "bg-slate-100 dark:bg-zinc-800 text-zinc-950 dark:text-zinc-50"
                        : "text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
                    )}
                    onClick={() => {
                      onSelectConversation(conversation.id ?? "");
                    }}
                    type="button"
                  >
                    <span className="block truncate font-semibold">
                      {conversation.title ||
                        conversation.id ||
                        "Untitled conversation"}
                    </span>
                    <span className="mt-1 flex items-center gap-2">
                      <ConversationStatusDot status={conversation.status} />
                      {conversation.daemonOnline === false ? (
                        <span className="shrink-0 rounded-full bg-amber-100 dark:bg-amber-950/45 px-2 py-0.5 text-[0.7rem] font-medium text-amber-700 dark:text-amber-300">
                          Offline
                        </span>
                      ) : null}
                      {conversation.updatedAt ? (
                        <Timestamp
                          className="truncate font-mono text-[0.7rem] text-slate-400 dark:text-zinc-500"
                          relative
                          value={conversation.updatedAt}
                        />
                      ) : null}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {hasSelectedConversation ? null : (
        <div className="mt-5 rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50/60 dark:bg-zinc-950/60 p-4 text-center lg:hidden">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
            No conversation selected
          </h2>
          <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {onlineDaemons.length
              ? "Start a new conversation with an online daemon, or pick one from the list."
              : "Run gs agent start to make an agent available, then create a conversation."}
          </p>
        </div>
      )}
    </aside>
  );
}

// Local components kept stateless so they don't re-render when the parent's
// selection state changes. Each row only depends on its own props.

function DaemonRow({ daemon }: { daemon: AgentDaemon }) {
  return (
    <div className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3">
      <div className="flex items-start justify-between gap-2">
        <p className="min-w-0 break-words text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          {daemonName(daemon, "Unnamed agent")}
        </p>
        <span className="inline-flex shrink-0 items-center gap-1.5 rounded-md bg-emerald-50 dark:bg-emerald-950/30 px-2 py-1 text-[0.7rem] font-semibold uppercase tracking-normal text-emerald-700 dark:text-emerald-300">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
          online
        </span>
      </div>
      <p className="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
        {daemon.runtime || "runtime"} {daemon.version || ""}
      </p>
    </div>
  );
}
