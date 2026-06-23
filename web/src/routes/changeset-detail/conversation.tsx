import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Ref
} from "react";
import { createPortal } from "react-dom";

import type { ConversationEvent, Patchset } from "../../api/types";
import { useApi } from "../../api/useApi";
import { useQuery } from "@tanstack/react-query";
import { cn } from "../../lib/cn";
import { patchsetConversationRange } from "./patchsetUtils";

export type ConversationViewItem =
  | { kind: "message"; event: ConversationEvent }
  | { kind: "trace"; events: ConversationEvent[] };

export interface PatchsetConversationProps {
  patchsets: Patchset[];
  selectedPatchsetId: string;
  fromPatchsetId: string;
  enabled: boolean;
}

export function ConversationDrawer({
  docked,
  enabled,
  fromPatchsetId,
  onClose,
  open,
  patchsets,
  selectedPatchsetId
}: PatchsetConversationProps & {
  docked: boolean;
  onClose(): void;
  open: boolean;
}) {
  const [entered, setEntered] = useState(false);
  const closeButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open || docked || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };

    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [docked, onClose, open]);

  useEffect(() => {
    if (!open || docked || typeof window === "undefined") {
      setEntered(false);
      return;
    }

    setEntered(false);
    const frame = window.requestAnimationFrame(() => setEntered(true));
    return () => window.cancelAnimationFrame(frame);
  }, [docked, open]);

  useEffect(() => {
    if (!open || docked) {
      return;
    }

    closeButtonRef.current?.focus();
  }, [docked, open]);

  if (!open) {
    return null;
  }

  const drawerContent = (
    <>
      <ConversationDrawerHeader
        closeButtonRef={closeButtonRef}
        onClose={onClose}
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        <PatchsetConversationContent
          enabled={enabled}
          fromPatchsetId={fromPatchsetId}
          patchsets={patchsets}
          selectedPatchsetId={selectedPatchsetId}
        />
      </div>
    </>
  );

  if (docked) {
    return (
      <aside className="mt-2.5 flex max-h-[calc(100dvh-2rem)] w-[24rem] shrink-0 flex-col overflow-y-auto rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 md:mt-3 xl:sticky xl:top-4 xl:self-start">
        {drawerContent}
      </aside>
    );
  }

  if (typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <div className="fixed inset-0 z-40">
      <button
        aria-label="Close conversation drawer"
        className={cn(
          "fixed inset-0 bg-black/30 transition-opacity duration-200",
          entered ? "opacity-100" : "opacity-0"
        )}
        onClick={onClose}
        type="button"
      />
      <aside
        aria-label="Agent conversation"
        aria-modal="true"
        className={cn(
          "fixed inset-y-0 right-0 z-10 flex w-full max-w-[28rem] transform flex-col overflow-hidden bg-white shadow-xl transition-transform duration-200",
          entered ? "translate-x-0" : "translate-x-full"
        )}
        role="dialog"
      >
        {drawerContent}
      </aside>
    </div>,
    document.body
  );
}

export function ConversationDrawerHeader({
  closeButtonRef,
  onClose
}: {
  closeButtonRef?: Ref<HTMLButtonElement>;
  onClose(): void;
}) {
  return (
    <div className="flex items-start justify-between gap-3 border-b border-slate-200 bg-white px-4 py-3">
      <div className="min-w-0">
        <h2 className="text-sm font-semibold text-zinc-950">
          Agent conversation
        </h2>
        <p className="mt-1 text-xs text-slate-500">
          The exchange behind the selected diff.
        </p>
      </div>
      <button
        aria-label="Close conversation"
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-slate-200 bg-white text-sm font-semibold text-slate-600 transition hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98]"
        onClick={onClose}
        ref={closeButtonRef}
        type="button"
      >
        ✕
      </button>
    </div>
  );
}

export function PatchsetConversationContent({
  patchsets,
  selectedPatchsetId,
  fromPatchsetId,
  enabled
}: PatchsetConversationProps) {
  const api = useApi();
  const range = useMemo(
    () => patchsetConversationRange(patchsets, selectedPatchsetId, fromPatchsetId),
    [patchsets, selectedPatchsetId, fromPatchsetId]
  );

  const eventsQuery = useQuery({
    enabled: Boolean(enabled && range),
    queryKey: [
      "conversationEvents",
      range?.conversationId ?? "",
      range?.afterSeq ?? 0,
      range?.beforeSeq ?? 0
    ],
    queryFn: () =>
      api.getConversationEvents({
        conversationId: range!.conversationId,
        afterSeq: range!.afterSeq,
        beforeSeq: range!.beforeSeq
      })
  });

  if (!range) {
    return (
      <p className="text-sm text-slate-600">
        This patchset was not produced by an agent conversation.
      </p>
    );
  }

  const events = eventsQuery.data?.events ?? [];
  const items = buildConversationView(events);

  return (
    <>
      {eventsQuery.isPending ? (
        <p className="text-sm text-slate-600">Loading conversation…</p>
      ) : eventsQuery.isError ? (
        <p className="text-sm text-rose-600">
          Could not load the conversation for this patchset.
        </p>
      ) : items.length === 0 ? (
        <p className="text-sm text-slate-600">No messages for this patchset.</p>
      ) : (
        <ul className="space-y-3">
          {items.map((item, index) =>
            item.kind === "trace" ? (
              <li key={`trace-${index}`}>
                <ConversationTrace events={item.events} />
              </li>
            ) : (
              <li
                key={item.event.id ?? `${item.event.seq}-${index}`}
                className={cn(
                  "rounded-lg border px-3 py-2 text-sm",
                  item.event.role === "user"
                    ? "border-zinc-200 bg-zinc-50"
                    : item.event.role === "system"
                      ? "border-amber-200 bg-amber-50"
                      : "border-slate-200 bg-white"
                )}
              >
                <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500">
                  {item.event.role ?? "agent"}
                  {item.event.type && item.event.type !== "message"
                    ? ` · ${item.event.type}`
                    : ""}
                </div>
                <div className="whitespace-pre-wrap break-words text-slate-800">
                  {item.event.text}
                </div>
              </li>
            )
          )}
        </ul>
      )}
    </>
  );
}

export function buildConversationView(
  events: ConversationEvent[]
): ConversationViewItem[] {
  const coalesced = coalesceStreamedEvents(events);
  const items: ConversationViewItem[] = [];
  let trace: ConversationEvent[] = [];

  const flushTrace = () => {
    if (trace.length) {
      items.push({ kind: "trace", events: trace });
      trace = [];
    }
  };

  for (const event of coalesced) {
    if (isTraceEvent(event)) {
      trace.push(event);
      continue;
    }
    flushTrace();
    items.push({ kind: "message", event });
  }
  flushTrace();
  return items;
}

export function coalesceStreamedEvents(
  events: ConversationEvent[]
): ConversationEvent[] {
  const finalizedKeys = new Set<string>();
  for (const event of events) {
    const type = (event.type ?? "").toLowerCase();
    if (event.itemId && !type.endsWith("_delta")) {
      finalizedKeys.add(`${event.itemId}:${type}`);
    }
  }

  const result: ConversationEvent[] = [];
  const snapshotIndex = new Map<string, number>();
  for (const event of events) {
    const type = (event.type ?? "").toLowerCase();
    if (!type.endsWith("_delta")) {
      result.push(event);
      continue;
    }
    if (event.itemId && finalizedKeys.has(`${event.itemId}:${type.slice(0, -6)}`)) {
      continue;
    }
    const key = `${event.itemId ?? ""}:${type}`;
    const existing = event.itemId ? snapshotIndex.get(key) : undefined;
    if (existing !== undefined) {
      result[existing] = event;
      continue;
    }
    if (event.itemId) {
      snapshotIndex.set(key, result.length);
    }
    result.push(event);
  }
  return result;
}

export function isTraceEvent(event: ConversationEvent) {
  const role = (event.role ?? "").toLowerCase();
  const type = (event.type ?? "").toLowerCase();
  return (
    role === "tool" ||
    type === "delta" ||
    type === "tool_call" ||
    type === "tool_output" ||
    type === "thinking" ||
    type === "thinking_trace" ||
    type === "reasoning" ||
    type === "reasoning_delta" ||
    type === "message_delta"
  );
}

export function ConversationTrace({ events }: { events: ConversationEvent[] }) {
  return (
    <details className="rounded-lg border border-slate-200 bg-slate-50/70 text-sm">
      <summary className="cursor-pointer select-none px-3 py-2 text-xs font-semibold uppercase tracking-wide text-slate-500 transition hover:text-zinc-950">
        Agent thinking ({events.length})
      </summary>
      <ul className="space-y-2 border-t border-slate-200 px-3 py-2">
        {events.map((event, index) => (
          <li key={event.id ?? `${event.seq}-${index}`}>
            <div className="mb-0.5 text-[0.7rem] font-semibold uppercase tracking-wide text-slate-400">
              {event.role ?? "agent"}
              {event.type ? ` · ${event.type}` : ""}
            </div>
            <div className="whitespace-pre-wrap break-words text-slate-600">
              {event.text || event.dataJson}
            </div>
          </li>
        ))}
      </ul>
    </details>
  );
}