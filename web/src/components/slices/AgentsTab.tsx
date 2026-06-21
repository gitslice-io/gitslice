import { FormEvent, useEffect, useMemo, useState } from "react";

import type {
  AgentDaemon,
  Conversation,
  SliceRef
} from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { AgentConversation } from "./AgentConversation";
import { SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

interface AgentsTabProps {
  api: ApiClient;
  slice: SliceRef;
}

export function AgentsTab({ api, slice }: AgentsTabProps) {
  const [daemons, setDaemons] = useState<AgentDaemon[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [selectedConversationId, setSelectedConversationId] = useState("");
  const [selectedDaemonId, setSelectedDaemonId] = useState("");
  const [title, setTitle] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [isCreating, setIsCreating] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [createError, setCreateError] = useState("");

  const sliceKey = `${slice.account ?? ""}:${slice.slice ?? ""}`;
  const onlineDaemons = useMemo(
    () => daemons.filter((daemon) => daemon.status === "online"),
    [daemons]
  );
  const selectedConversation = conversations.find(
    (conversation) => conversation.id === selectedConversationId
  );

  useEffect(() => {
    let cancelled = false;

    async function loadAgents() {
      setIsLoading(true);
      setLoadError("");
      try {
        const [daemonResponse, conversationResponse] = await Promise.all([
          api.listDaemons({}),
          api.listConversations({ slice })
        ]);

        if (cancelled) {
          return;
        }

        const nextDaemons = daemonResponse.daemons ?? [];
        const nextConversations = sortConversationsNewestFirst(
          conversationResponse.conversations ?? []
        );
        const nextOnlineDaemons = nextDaemons.filter(
          (daemon) => daemon.status === "online"
        );

        setDaemons(nextDaemons);
        setConversations(nextConversations);
        setSelectedDaemonId((current) =>
          nextOnlineDaemons.some((daemon) => daemon.id === current)
            ? current
            : nextOnlineDaemons[0]?.id ?? ""
        );
        setSelectedConversationId((current) =>
          current &&
          nextConversations.some((conversation) => conversation.id === current)
            ? current
            : nextConversations[0]?.id ?? ""
        );
      } catch (error) {
        if (!cancelled) {
          setLoadError(getErrorMessage(error));
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    }

    void loadAgents();

    return () => {
      cancelled = true;
    };
  }, [api, slice, sliceKey]);

  async function createConversation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedDaemonId) {
      return;
    }

    setIsCreating(true);
    setCreateError("");
    try {
      const conversation = await api.createConversation({
        daemonId: selectedDaemonId,
        slice,
        title: title.trim() || undefined
      });

      setConversations((current) => [
        conversation,
        ...current.filter((item) => item.id !== conversation.id)
      ]);
      setSelectedConversationId(conversation.id ?? "");
      setTitle("");
    } catch (error) {
      setCreateError(getErrorMessage(error));
    } finally {
      setIsCreating(false);
    }
  }

  if (!slice.account || !slice.slice) {
    return (
      <SliceNotice title="Missing slice" tone="error">
        Agent conversations need an account and slice name.
      </SliceNotice>
    );
  }

  if (isLoading) {
    return (
      <SlicePanel className="min-h-[28rem]">
        <div className="grid gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
          <div className="grid gap-3">
            <div className="h-8 animate-pulse rounded-md bg-slate-200" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100" />
          </div>
          <div className="h-72 animate-pulse rounded-md bg-slate-100" />
        </div>
      </SlicePanel>
    );
  }

  if (loadError) {
    return (
      <SliceNotice title="Could not load agents" tone="error">
        {loadError}
      </SliceNotice>
    );
  }

  return (
    <SlicePanel className="min-h-[34rem] p-0 lg:h-full lg:min-h-0">
      <div className="grid h-full min-h-0 gap-0 lg:grid-cols-[21rem_minmax(0,1fr)]">
        <aside className="border-b border-slate-200 p-4 lg:min-h-0 lg:border-b-0 lg:border-r lg:p-5">
          <div>
            <h2 className="text-sm font-semibold text-zinc-950">Agents</h2>
            <p className="mt-1 text-xs leading-5 text-slate-500">
              Online daemons for {sliceKey}
            </p>
          </div>

          {onlineDaemons.length === 0 ? (
            <div className="mt-4 rounded-md border border-dashed border-slate-300 bg-slate-50 p-4 text-sm text-slate-700">
              <p className="font-semibold text-zinc-950">No online daemons</p>
              <p className="mt-2 leading-6">
                Run{" "}
                <code className="rounded bg-white px-1.5 py-0.5 font-mono text-xs text-slate-800">
                  gs agent start
                </code>{" "}
                in an empty directory to make an agent available here.
              </p>
            </div>
          ) : (
            <div className="mt-4 grid gap-2">
              {onlineDaemons.map((daemon) => (
                <div
                  className="rounded-md border border-slate-200 bg-white px-3 py-3"
                  key={daemon.id ?? daemon.name}
                >
                  <div className="flex items-center justify-between gap-3">
                    <p className="min-w-0 truncate text-sm font-semibold text-zinc-950">
                      {daemon.name || daemon.id || "Unnamed agent"}
                    </p>
                    <span className="inline-flex items-center gap-1.5 rounded-md bg-emerald-50 px-2 py-1 text-[0.7rem] font-semibold uppercase tracking-normal text-emerald-700">
                      <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                      online
                    </span>
                  </div>
                  <p className="mt-1 truncate font-mono text-xs text-slate-500">
                    {daemon.runtime || "runtime"} {daemon.version || ""}
                  </p>
                </div>
              ))}
            </div>
          )}

          {onlineDaemons.length ? (
            <form className="mt-5 grid gap-3" onSubmit={createConversation}>
              <label className="grid gap-1.5 text-sm font-medium text-zinc-800">
                Agent daemon
                <select
                  className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                  onChange={(event) => setSelectedDaemonId(event.target.value)}
                  value={selectedDaemonId}
                >
                  {onlineDaemons.map((daemon) => (
                    <option
                      key={daemon.id ?? daemon.name}
                      value={daemon.id ?? ""}
                    >
                      {daemon.name || daemon.id}
                    </option>
                  ))}
                </select>
              </label>
              <label className="grid gap-1.5 text-sm font-medium text-zinc-800">
                Title
                <input
                  className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                  onChange={(event) => setTitle(event.target.value)}
                  placeholder="Optional"
                  value={title}
                />
              </label>
              {createError ? (
                <p className="text-sm text-rose-700">{createError}</p>
              ) : null}
              <button
                className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
                disabled={!selectedDaemonId || isCreating}
                type="submit"
              >
                {isCreating ? "Creating..." : "New conversation"}
              </button>
            </form>
          ) : null}

          <div className="mt-6 border-t border-slate-200 pt-4">
            <h3 className="text-xs font-semibold uppercase tracking-normal text-slate-500">
              Conversations
            </h3>
            {conversations.length === 0 ? (
              <p className="mt-3 rounded-md border border-dashed border-slate-300 p-3 text-sm text-slate-600">
                No conversations yet.
              </p>
            ) : (
              <div className="mt-3 grid gap-1.5">
                {conversations.map((conversation) => (
                  <button
                    className={cn(
                      "min-w-0 rounded-md px-3 py-2 text-left text-sm transition active:scale-[0.98]",
                      conversation.id === selectedConversationId
                        ? "bg-slate-100 text-zinc-950"
                        : "text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
                    )}
                    key={conversation.id}
                    onClick={() =>
                      setSelectedConversationId(conversation.id ?? "")
                    }
                    type="button"
                  >
                    <span className="block truncate font-semibold">
                      {conversation.title ||
                        conversation.id ||
                        "Untitled conversation"}
                    </span>
                    <span className="mt-1 block truncate font-mono text-xs text-slate-500">
                      {conversation.status || "active"}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </aside>

        <div className="min-h-[26rem] min-w-0 lg:min-h-0">
          {selectedConversationId ? (
            <AgentConversation
              api={api}
              conversation={selectedConversation}
              conversationId={selectedConversationId}
            />
          ) : (
            <div className="flex h-full min-h-[26rem] items-center justify-center p-6 text-center">
              <div className="max-w-sm">
                <h2 className="text-base font-semibold text-zinc-950">
                  No conversation selected
                </h2>
                <p className="mt-2 text-sm leading-6 text-slate-600">
                  Create a conversation with an online daemon to start sending
                  messages.
                </p>
              </div>
            </div>
          )}
        </div>
      </div>
    </SlicePanel>
  );
}

function sortConversationsNewestFirst(conversations: Conversation[]) {
  return [...conversations].sort((left, right) => {
    const leftTime = conversationSortTime(left);
    const rightTime = conversationSortTime(right);
    if (leftTime !== rightTime) {
      return rightTime - leftTime;
    }
    return (right.id ?? "").localeCompare(left.id ?? "");
  });
}

function conversationSortTime(conversation: Conversation) {
  const created = Date.parse(conversation.createdAt ?? "");
  if (Number.isFinite(created)) {
    return created;
  }
  const updated = Date.parse(conversation.updatedAt ?? "");
  return Number.isFinite(updated) ? updated : 0;
}
