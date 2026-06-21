import {
  FormEvent,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";

import type { Conversation, ConversationEvent } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "./SlicePageParts";

interface AgentConversationProps {
  api: ApiClient;
  conversation?: Conversation;
  conversationId: string;
}

export function AgentConversation({
  api,
  conversation,
  conversationId
}: AgentConversationProps) {
  const [events, setEvents] = useState<ConversationEvent[]>([]);
  const [draft, setDraft] = useState("");
  const [sendError, setSendError] = useState("");
  const [streamError, setStreamError] = useState("");
  const [isSending, setIsSending] = useState(false);
  const endRef = useRef<HTMLDivElement>(null);

  const title = useMemo(
    () => conversation?.title || conversationId,
    [conversation?.title, conversationId]
  );

  useEffect(() => {
    setEvents([]);
    setStreamError("");
    const controller = new AbortController();

    async function readStream() {
      try {
        for await (const event of api.streamConversation(
          { conversationId, afterSeq: 0 },
          controller.signal
        )) {
          if (controller.signal.aborted) {
            return;
          }
          setEvents((current) => appendConversationEvent(current, event));
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setStreamError(getErrorMessage(error));
        }
      }
    }

    void readStream();

    return () => {
      controller.abort();
    };
  }, [api, conversationId]);

  useEffect(() => {
    endRef.current?.scrollIntoView?.({ block: "end" });
  }, [events.length]);

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const text = draft.trim();
    if (!text || isSending) {
      return;
    }

    setIsSending(true);
    setSendError("");
    try {
      const response = await api.sendAgentMessage({ conversationId, text });
      const sentEvent = response.event;
      if (sentEvent) {
        setEvents((current) =>
          appendConversationEvent(current, sentEvent)
        );
      }
      setDraft("");
    } catch (error) {
      setSendError(getErrorMessage(error));
    } finally {
      setIsSending(false);
    }
  }

  return (
    <div className="flex h-full min-h-[26rem] flex-col">
      <div className="border-b border-slate-200 px-4 py-4 sm:px-5">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <h2 className="truncate text-base font-semibold text-zinc-950">
              {title}
            </h2>
            {conversation?.workspaceSubdir ? (
              <p className="mt-1 truncate font-mono text-xs text-slate-500">
                {conversation.workspaceSubdir}
              </p>
            ) : null}
          </div>
          <span className="inline-flex w-fit rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-600">
            {conversation?.status || "active"}
          </span>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto bg-slate-50/60 px-4 py-4 sm:px-5">
        {streamError ? (
          <div className="mb-4 rounded-md border border-rose-200 bg-rose-50 p-3 text-sm text-rose-900">
            <p className="font-semibold">Stream stopped</p>
            <p className="mt-1 leading-6">{streamError}</p>
          </div>
        ) : null}

        {events.length === 0 ? (
          <div className="flex min-h-64 items-center justify-center text-center">
            <div className="max-w-sm">
              <h3 className="text-sm font-semibold text-zinc-950">
                No messages yet
              </h3>
              <p className="mt-2 text-sm leading-6 text-slate-600">
                Send a message to start this agent conversation.
              </p>
            </div>
          </div>
        ) : (
          <div className="grid gap-3">
            {events.map((event, index) => (
              <ConversationEventBubble
                event={event}
                key={eventKey(event, index)}
              />
            ))}
          </div>
        )}
        <div ref={endRef} />
      </div>

      <form
        className="border-t border-slate-200 bg-white px-4 py-4 sm:px-5"
        onSubmit={sendMessage}
      >
        <label className="grid gap-2 text-sm font-medium text-zinc-800">
          Message
          <textarea
            className="max-h-48 min-h-24 w-full min-w-0 resize-y rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Ask the agent to inspect or edit this slice"
            value={draft}
          />
        </label>
        {sendError ? (
          <p className="mt-2 text-sm text-rose-700">{sendError}</p>
        ) : null}
        <div className="mt-3 flex justify-end">
          <button
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
            disabled={isSending || !draft.trim()}
            type="submit"
          >
            {isSending ? "Sending..." : "Send"}
          </button>
        </div>
      </form>
    </div>
  );
}

function ConversationEventBubble({ event }: { event: ConversationEvent }) {
  const role = event.role || "system";
  const isUser = role === "user";
  const isAgent = role === "agent";
  const content = event.text || event.dataJson || event.type || "";
  const capturedPatchset = parseCapturedPatchset(event);

  return (
    <article
      className={cn(
        "max-w-[min(44rem,85%)] rounded-md px-3 py-2 text-sm leading-6 shadow-sm",
        isUser && "ml-auto bg-zinc-950 text-white shadow-slate-900/10",
        isAgent &&
          "mr-auto border border-slate-200 bg-white text-zinc-950 shadow-slate-200/60",
        !isUser &&
          !isAgent &&
          "mx-auto border border-slate-200 bg-white text-slate-700 shadow-slate-200/60"
      )}
    >
      <div
        className={cn(
          "mb-1 flex items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal",
          isUser ? "text-slate-300" : "text-slate-500"
        )}
      >
        <span>{role}</span>
        {event.type ? <span>{event.type}</span> : null}
        {hasSequence(event) ? (
          <span className="text-slate-400">#{event.seq}</span>
        ) : null}
      </div>
      {capturedPatchset ? (
        <div className="grid gap-2">
          <p className="font-medium text-zinc-950">
            Captured patchset {capturedPatchset.patchsetNumber}
          </p>
          <a
            className="inline-flex w-fit max-w-full items-center rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs font-semibold text-zinc-900 underline decoration-slate-300 underline-offset-4 transition hover:border-slate-400 hover:decoration-slate-700 active:scale-[0.98]"
            href={`/cs/${encodeURIComponent(capturedPatchset.changesetId)}`}
          >
            <span className="truncate">
              changeset {capturedPatchset.changesetId}
            </span>
          </a>
        </div>
      ) : event.dataJson && !event.text ? (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-100 p-2 font-mono text-xs text-slate-700">
          {event.dataJson}
        </pre>
      ) : (
        <p className="whitespace-pre-wrap break-words">{content}</p>
      )}
    </article>
  );
}

function appendConversationEvent(
  current: ConversationEvent[],
  event: ConversationEvent
) {
  const identity = eventIdentity(event);
  if (identity && current.some((item) => eventIdentity(item) === identity)) {
    return current;
  }

  const next = [...current, event];
  if (hasSequence(event)) {
    next.sort(compareConversationEvents);
  }
  return next;
}

function compareConversationEvents(
  left: ConversationEvent,
  right: ConversationEvent
) {
  const leftSeq = Number(left.seq);
  const rightSeq = Number(right.seq);
  if (Number.isFinite(leftSeq) && Number.isFinite(rightSeq)) {
    return leftSeq - rightSeq;
  }
  return 0;
}

function eventIdentity(event: ConversationEvent) {
  if (event.id) {
    return `id:${event.id}`;
  }
  if (event.conversationId && hasSequence(event)) {
    return `seq:${event.conversationId}:${event.seq}`;
  }
  return "";
}

function eventKey(event: ConversationEvent, index: number) {
  return eventIdentity(event) || `${event.role ?? "event"}-${index}`;
}

function hasSequence(event: ConversationEvent) {
  return event.seq !== undefined && event.seq !== "";
}

function parseCapturedPatchset(event: ConversationEvent) {
  if (event.role !== "system" || event.type !== "status" || !event.text) {
    return null;
  }

  const match = event.text
    .trim()
    .match(/^captured changeset ([^\s]+) patchset ([1-9][0-9]*)$/);
  if (!match) {
    return null;
  }

  return {
    changesetId: match[1],
    patchsetNumber: match[2]
  };
}
