import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useAuth } from "@clerk/tanstack-react-start";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type PointerEvent,
  type Ref,
  type ReactNode
} from "react";
import { createPortal } from "react-dom";

import type { Changeset, ConversationEvent, Patchset } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { DiffViewer } from "../components/diff/DiffViewer";
import { cn } from "../lib/cn";
import { shortChangesetId, shortHash } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { displaySubmitBlockedReason } from "./stackPageUtils";

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const params = useParams({ strict: false }) as { id?: string };
  const changesetId = params.id ?? "";
  const [abandonReason, setAbandonReason] = useState("");
  const [actionError, setActionError] = useState("");
  const [conversationOpen, setConversationOpen] = useState(false);
  const [fromPatchset, setFromPatchset] = useState("");
  const [isWide, setIsWide] = useState(() =>
    typeof window === "undefined" || typeof window.matchMedia !== "function"
      ? false
      : window.matchMedia("(min-width: 1280px)").matches
  );
  const [toPatchset, setToPatchset] = useState("");

  useEffect(() => {
    if (
      typeof window === "undefined" ||
      typeof window.matchMedia !== "function"
    ) {
      return;
    }

    const wideQuery = window.matchMedia("(min-width: 1280px)");
    const updateIsWide = () => {
      setIsWide(wideQuery.matches);
    };

    updateIsWide();
    wideQuery.addEventListener("change", updateIsWide);

    return () => {
      wideQuery.removeEventListener("change", updateIsWide);
    };
  }, []);

  const changesetQuery = useQuery({
    // Wait for Clerk to resolve before fetching: this page is public, but a
    // signed-in user viewing a private changeset needs the auth token attached
    // on the first request. Firing before `isLoaded` would send a tokenless
    // request that React Query never retries (the token isn't in the key).
    enabled: Boolean(isLoaded && changesetId),
    queryKey: ["changeset", changesetId],
    queryFn: () => api.getChangeset({ changesetId }),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
  const authoringSlice = changeset?.authoringSlice;

  // Dependent (child) changesets are not carried on the changeset itself.
  // Resolve them from the slice's changesets (anonymously readable for public
  // slices, like the base-changeset field) and keep those based on this one.
  const sliceChangesetsQuery = useQuery({
    enabled: Boolean(
      changeset?.id && authoringSlice?.account && authoringSlice?.slice
    ),
    queryKey: [
      "changesetsBySlice",
      authoringSlice?.account,
      authoringSlice?.slice
    ],
    queryFn: () => api.listChangesets({ authoringSlice, limit: 200 })
  });

  const dependentChangesets = useMemo(() => {
    const selfId = changeset?.id;
    if (!selfId) {
      return [] as { id: string; title: string }[];
    }
    return (sliceChangesetsQuery.data?.changesets ?? [])
      .filter((candidate) => candidate.parentChangesetId === selfId && candidate.id)
      .map((candidate) => ({
        id: candidate.id as string,
        title: candidate.title ?? ""
      }));
  }, [sliceChangesetsQuery.data?.changesets, changeset?.id]);

  const canonicalChangesetId = changeset?.id || changesetId;
  const sliceSearch = changeset ? changesetSliceSearch(changeset) : "";
  const patchsets = useMemo(() => sortedPatchsets(changeset), [changeset]);
  const patchsetIdsKey = patchsets.map((patchset) => patchset.id || "").join("|");
  const selectedToPatchset =
    toPatchset ||
    changeset?.currentPatchsetId ||
    patchsets[patchsets.length - 1]?.id ||
    "";

  useEffect(() => {
    if (!changeset) {
      setFromPatchset("");
      setToPatchset("");
      return;
    }

    const ids = new Set(
      patchsets
        .map((patchset) => patchset.id)
        .filter((id): id is string => Boolean(id))
    );
    const defaultTo =
      changeset.currentPatchsetId || patchsets[patchsets.length - 1]?.id || "";

    setFromPatchset((current) =>
      current === "" || ids.has(current) ? current : ""
    );
    setToPatchset((current) => (current && ids.has(current) ? current : defaultTo));
  }, [changeset, patchsetIdsKey, patchsets]);

  const diffQuery = useQuery({
    enabled: Boolean(changeset && canonicalChangesetId),
    queryKey: [
      "changesetDiff",
      canonicalChangesetId,
      fromPatchset,
      selectedToPatchset
    ],
    queryFn: () =>
      api.diffChangeset({
        changesetId: canonicalChangesetId,
        fromPatchset: fromPatchset || undefined,
        toPatchset: selectedToPatchset || undefined
      })
  });

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };
  const closeConversation = useCallback(() => setConversationOpen(false), []);

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }
      if (!changeset?.currentPatchsetId) {
        throw new Error("This changeset has no current patchset to merge.");
      }

      return api.submitChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const abandonMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.abandonChangeset({
        changesetId: canonicalChangesetId,
        reason: abandonReason.trim()
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      setAbandonReason("");
      await invalidateChangeset();
    }
  });

  const submitAbandon = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    abandonMutation.mutate();
  };

  if (!changesetId) {
    return (
      <PageMessage
        title="Missing changeset"
        message="No changeset id was provided."
      />
    );
  }

  // `isPending` (not `isLoading`) so the disabled-pre-auth state counts as
  // loading; otherwise execution falls through to "Changeset not found" on
  // first paint and flashes once auth resolves.
  if (!isLoaded || changesetQuery.isPending) {
    return <ChangesetSkeleton />;
  }

  if (changesetQuery.isError) {
    return (
      <PageMessage
        title="Unable to load changeset"
        message={errorMessage(changesetQuery.error)}
      />
    );
  }

  if (!changeset) {
    return (
      <PageMessage
        title="Changeset not found"
        message="The API returned no changeset."
      />
    );
  }

  const terminal = isTerminalStatus(changeset.status);
  const actionBusy = mergeMutation.isPending || abandonMutation.isPending;
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-3 hidden sm:block">
        <Breadcrumb
          items={changesetBreadcrumbItems({
            changeset,
            sliceSearch
          })}
        />
      </div>

      <HeaderCard
        abandonReason={abandonReason}
        actionBusy={actionBusy}
        actionError={actionError}
        abandonPending={abandonMutation.isPending}
        canUseReviewActions={Boolean(isLoaded && isSignedIn)}
        changeset={changeset}
        dependentChangesets={dependentChangesets}
        mergePending={mergeMutation.isPending}
        onAbandon={submitAbandon}
        onAbandonReasonChange={setAbandonReason}
        onMerge={() => mergeMutation.mutate()}
        terminal={terminal}
      />

      <PatchsetComparePanel
        currentPatchsetId={changeset.currentPatchsetId}
        fromPatchset={fromPatchset}
        onFromPatchsetChange={setFromPatchset}
        onToPatchsetChange={setToPatchset}
        patchsets={patchsets}
        toPatchset={selectedToPatchset}
      />

      <div className={cn(isWide && conversationOpen && "flex items-start gap-3")}>
        <div className={cn(isWide && conversationOpen && "min-w-0 flex-1")}>
          <DiffViewer
            diffResponse={diffQuery.data}
            error={diffQuery.error}
            isError={diffQuery.isError}
            isLoading={diffQuery.isPending}
          />
        </div>
        <ConversationDrawer
          docked={isWide}
          enabled={Boolean(isLoaded)}
          onClose={closeConversation}
          open={conversationOpen}
          patchsets={patchsets}
          selectedPatchsetId={selectedToPatchset}
        />
      </div>

      {!conversationOpen ? (
        <button
          aria-expanded={conversationOpen}
          className="fixed bottom-4 right-4 z-30 inline-flex h-10 items-center gap-2 rounded-full border border-slate-200 bg-zinc-950 px-4 text-sm font-medium text-white shadow-lg shadow-slate-900/20 transition hover:bg-zinc-800 active:scale-[0.98]"
          onClick={() => setConversationOpen(true)}
          type="button"
        >
          <span aria-hidden="true">💬</span>
          <span>Conversation</span>
        </button>
      ) : null}
    </section>
  );
}

function patchsetConversationRange(
  patchsets: Patchset[],
  selectedPatchsetId: string
): { conversationId: string; afterSeq: number; beforeSeq: number } | null {
  const selected = patchsets.find((patchset) => patchset.id === selectedPatchsetId);
  const conversationId = selected?.authoringConversationId ?? "";
  if (!selected || !conversationId) {
    return null;
  }
  const beforeSeq = Number(selected.authoringConversationSeq ?? 0);
  const selectedNumber = Number(selected.number ?? 0);
  // The exchange behind this patchset starts after the previous patchset that
  // came from the same conversation (the cutoffs are recorded server-side).
  let afterSeq = 0;
  for (const patchset of patchsets) {
    if (patchset.authoringConversationId !== conversationId) {
      continue;
    }
    const number = Number(patchset.number ?? 0);
    if (number < selectedNumber) {
      afterSeq = Math.max(afterSeq, Number(patchset.authoringConversationSeq ?? 0));
    }
  }
  return { conversationId, afterSeq, beforeSeq };
}

type PatchsetConversationProps = {
  patchsets: Patchset[];
  selectedPatchsetId: string;
  enabled: boolean;
};

function ConversationDrawer({
  docked,
  enabled,
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

function ConversationDrawerHeader({
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
          The exchange that produced this patchset.
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

function PatchsetConversationContent({
  patchsets,
  selectedPatchsetId,
  enabled
}: PatchsetConversationProps) {
  const api = useApi();
  const range = useMemo(
    () => patchsetConversationRange(patchsets, selectedPatchsetId),
    [patchsets, selectedPatchsetId]
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
  // Persisted streams carry every cumulative `*_delta` snapshot (the growing
  // "Thanks", "Thanks. Send me the", … prefixes of one streamed message). Drop
  // those superseded snapshots and tuck reasoning/tool trace behind a collapsed
  // disclosure, so the panel reads as the finalized exchange — not the raw token
  // stream that produced it.
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

type ConversationViewItem =
  | { kind: "message"; event: ConversationEvent }
  | { kind: "trace"; events: ConversationEvent[] };

// buildConversationView turns the raw persisted event stream into what the
// patchset panel should actually show: finalized messages inline, with
// reasoning/tool trace grouped behind a collapsed disclosure. The streamed
// `*_delta` snapshots are cumulative, so a finalized event supersedes its own
// snapshots and only the latest snapshot survives when no final arrived.
function buildConversationView(
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

// coalesceStreamedEvents drops cumulative `*_delta` snapshots that a finalized
// event of the same item already covers, and collapses any remaining snapshots
// for an item to their latest (most complete) entry.
function coalesceStreamedEvents(
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
  // Where the latest snapshot for a given delta item currently sits, so a newer
  // snapshot can replace it in place rather than appending a duplicate.
  const snapshotIndex = new Map<string, number>();
  for (const event of events) {
    const type = (event.type ?? "").toLowerCase();
    if (!type.endsWith("_delta")) {
      result.push(event);
      continue;
    }
    // A finalized event for this item supersedes its snapshots entirely.
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

function isTraceEvent(event: ConversationEvent) {
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

// ConversationTrace collapses the reasoning/tool activity between two messages
// into a single disclosure, so the panel defaults to the finalized exchange.
function ConversationTrace({ events }: { events: ConversationEvent[] }) {
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

function HeaderCard({
  abandonPending,
  abandonReason,
  actionBusy,
  actionError,
  changeset,
  dependentChangesets,
  canUseReviewActions,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  actionError: string;
  changeset: Changeset;
  dependentChangesets: { id: string; title: string }[];
  canUseReviewActions: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  // On small screens the diff is the priority, so the description and base
  // commit collapse behind a toggle. From `lg` up they are always shown (the
  // toggle is hidden). Review actions — Merge plus the overflow menu — stay
  // visible at every size so merging is never more than one tap away.
  const [showDetails, setShowDetails] = useState(false);
  const publishing = isPublishing(changeset.status);
  const hasExpandableContent = Boolean(
    changeset.description || changeset.baseCommitId
  );

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="px-3 py-2.5 md:px-5 md:py-4">
        {publishing ? (
          <div className="mb-2.5 flex items-center gap-2 rounded-md border border-amber-200 bg-amber-50 px-2.5 py-1.5 text-xs font-medium text-amber-900 md:mb-3 md:px-3 md:py-2 md:text-sm">
            <span
              aria-hidden="true"
              className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500"
            />
            Publishing changeset…
          </div>
        ) : null}
        <div className="flex flex-col gap-2.5 lg:flex-row lg:items-start lg:justify-between lg:gap-3">
          <div className="min-w-0">
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 md:gap-3">
              <h1
                className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg md:text-2xl"
                title={changeset.title || "Untitled changeset"}
              >
                {changeset.title || "Untitled changeset"}
              </h1>
              {hasExpandableContent ? (
                <button
                  aria-expanded={showDetails}
                  aria-label={showDetails ? "Hide details" : "Show details"}
                  className="shrink-0 rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950 lg:hidden"
                  onClick={() => setShowDetails((value) => !value)}
                  type="button"
                >
                  {showDetails ? "Hide" : "Details"}
                </button>
              ) : null}
            </div>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11px] text-slate-600 md:mt-2 md:gap-x-2 md:gap-y-1.5 md:text-xs">
              <span
                className="max-w-[12rem] truncate rounded bg-slate-100 px-1.5 py-0.5 font-mono text-slate-700 md:px-2 md:py-1"
                title={changesetLabel(changeset)}
              >
                {changesetLabel(changeset)}
              </span>
              <StatusBadge status={changeset.status} />
              {changeset.parentChangesetId ? (
                <Link
                  className="inline-flex max-w-[12rem] items-center gap-1 truncate rounded border border-slate-200 bg-white px-1.5 py-0.5 font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950 md:max-w-[14rem] md:px-2 md:py-1"
                  params={{
                    id:
                      shortChangesetId(changeset.parentChangesetId) ||
                      changeset.parentChangesetId
                  }}
                  title={`Base changeset ${changeset.parentChangesetId}`}
                  to="/cs/$id"
                >
                  <span className="shrink-0">Base changeset</span>
                  <span className="truncate font-mono">
                    {shortChangesetId(changeset.parentChangesetId) ||
                      changeset.parentChangesetId}
                  </span>
                </Link>
              ) : null}
              {dependentChangesets.map((dependent) => (
                <Link
                  className="inline-flex max-w-[12rem] items-center gap-1 truncate rounded border border-slate-200 bg-white px-1.5 py-0.5 font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950 md:max-w-[14rem] md:px-2 md:py-1"
                  key={dependent.id}
                  params={{ id: shortChangesetId(dependent.id) || dependent.id }}
                  title={
                    dependent.title
                      ? `Dependent changeset ${dependent.id} — ${dependent.title}`
                      : `Dependent changeset ${dependent.id}`
                  }
                  to="/cs/$id"
                >
                  <span className="shrink-0">Dependent</span>
                  <span className="truncate font-mono">
                    {shortChangesetId(dependent.id) || dependent.id}
                  </span>
                </Link>
              ))}
              <CopyLinkButton changesetId={changeset.id || ""} />
            </div>
            <ChangesetMetaLine changeset={changeset} />
            <div className={cn("lg:block", showDetails ? "block" : "hidden")}>
              {changeset.description ? (
                <p className="mt-3 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-slate-700 md:mt-4">
                  {changeset.description}
                </p>
              ) : null}
              {changeset.baseCommitId ? (
                <p
                  className="mt-3 font-mono text-xs text-slate-500 md:mt-4"
                  title={changeset.baseCommitId}
                >
                  base {shortCommit(changeset.baseCommitId)}
                </p>
              ) : null}
            </div>
          </div>

          {canUseReviewActions ? (
            <div className="w-full shrink-0 lg:w-auto">
              <ReviewActions
                abandonPending={abandonPending}
                abandonReason={abandonReason}
                actionBusy={actionBusy}
                canMerge={
                  Boolean(changeset.currentPatchsetId) &&
                  isMergeableStatus(changeset.status)
                }
                mergePending={mergePending}
                onAbandon={onAbandon}
                onAbandonReasonChange={onAbandonReasonChange}
                onMerge={onMerge}
                terminal={terminal}
              />
            </div>
          ) : null}
        </div>

        {changeset.submitBlockedReason ? (
          <div className="mt-2.5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 md:mt-3 md:text-sm">
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
          </div>
        ) : null}
        {actionError ? (
          <ErrorBox className="mt-4 md:mt-5" message={actionError} />
        ) : null}
      </div>
    </div>
  );
}

function ReviewActions({
  abandonPending,
  abandonReason,
  actionBusy,
  canMerge,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  canMerge: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 lg:justify-end">
      <button
        className="h-8 rounded-md bg-zinc-950 px-3 py-2 text-xs font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={actionBusy || terminal || !canMerge}
        onClick={onMerge}
        type="button"
      >
        {mergePending ? "Merging..." : "Merge"}
      </button>

      {!terminal ? (
        <MoreActionsMenu disabled={actionBusy} label="More changeset actions">
          <form className="space-y-2" onSubmit={onAbandon}>
            <label className="block text-xs font-medium text-slate-600">
              Abandon changeset
              <input
                className="mt-1 h-9 w-full rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                disabled={actionBusy}
                onChange={(event) => onAbandonReasonChange(event.target.value)}
                placeholder="Optional reason"
                value={abandonReason}
              />
            </label>
            <button
              className={cn(dangerButtonClass, "w-full justify-center")}
              disabled={actionBusy}
              type="submit"
            >
              {abandonPending ? "Abandoning..." : "Abandon"}
            </button>
          </form>
        </MoreActionsMenu>
      ) : null}
    </div>
  );
}

// A compact overflow menu for actions that are run rarely (today: abandon), so
// the header surfaces the primary Merge button without crowding it with controls
// reviewers seldom reach for. Closes on outside click, Escape, scroll, or resize.
function MoreActionsMenu({
  children,
  disabled,
  label = "More actions"
}: {
  children: ReactNode;
  disabled?: boolean;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState({ top: 0, left: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) {
      return;
    }

    const margin = 8;
    const trigger = triggerRef.current.getBoundingClientRect();
    const menu = menuRef.current.getBoundingClientRect();
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;

    let left = trigger.right - menu.width;
    left = Math.min(
      Math.max(margin, left),
      Math.max(margin, viewportWidth - menu.width - margin)
    );

    let top = trigger.bottom + 4;
    if (top + menu.height > viewportHeight - margin) {
      top = Math.max(margin, trigger.top - menu.height - 4);
    }

    setCoords({ top: Math.round(top), left: Math.round(left) });
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const onPointerDown = (event: globalThis.MouseEvent) => {
      const target = event.target;
      if (
        target instanceof Node &&
        !triggerRef.current?.contains(target) &&
        !menuRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onReflow = () => {
      setOpen(false);
    };

    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);

    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
    };
  }, [open]);

  return (
    <>
      <button
        ref={triggerRef}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={label}
        className="inline-flex h-8 items-center gap-1 rounded-md border border-slate-300 bg-white px-3 py-2 text-xs font-medium text-slate-700 transition hover:border-slate-400 hover:bg-slate-50 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
        type="button"
      >
        <span>More</span>
        <span
          aria-hidden="true"
          className={cn(
            "text-[10px] leading-none transition-transform",
            open && "rotate-180"
          )}
        >
          ▾
        </span>
      </button>
      {open
        ? createPortal(
            <div
              className="fixed z-50 w-72 max-w-[calc(100vw-1rem)] rounded-md border border-slate-200 bg-white p-3 shadow-lg shadow-slate-900/10"
              ref={menuRef}
              role="menu"
              style={{ top: coords.top, left: coords.left }}
            >
              {children}
            </div>,
            document.body
          )
        : null}
    </>
  );
}

function PatchsetComparePanel({
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  patchsets,
  toPatchset
}: {
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const fromLabel = fromPatchset
    ? patchsetOptionLabel(findPatchset(patchsets, fromPatchset))
    : "Recorded base";
  const toLabel = patchsetOptionLabel(findPatchset(patchsets, toPatchset));

  return (
    <section className="mt-2.5 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 md:mt-3">
      <div className="flex items-center justify-between gap-2 px-3 py-2.5 md:px-5 md:py-3">
        <button
          aria-controls="patchset-timeline"
          aria-expanded={mobileOpen}
          className="-mx-1 flex min-w-0 flex-1 items-center gap-2 rounded px-1 text-left lg:cursor-default lg:pointer-events-none"
          onClick={() => setMobileOpen((value) => !value)}
          type="button"
        >
          <h2 className="text-[11px] font-semibold uppercase tracking-normal text-slate-500 md:text-xs">
            Patchsets
          </h2>
          <span
            aria-hidden="true"
            className={cn(
              "inline-block shrink-0 text-[10px] text-slate-400 transition-transform lg:hidden",
              mobileOpen && "rotate-90"
            )}
          >
            ▶
          </span>
          <p className="min-w-0 truncate text-[11px] font-medium text-zinc-950 md:text-xs">
            <span className="text-slate-500">{fromLabel}</span>
            <span className="mx-1 text-slate-400">→</span>
            <span>{toLabel || "selected patchset"}</span>
          </p>
        </button>
      </div>
      <div
        className={cn(
          "px-3 pb-2 md:px-5 md:pb-3",
          mobileOpen ? "block" : "hidden lg:block"
        )}
        id="patchset-timeline"
      >
        <PatchsetTimeline
          currentPatchsetId={currentPatchsetId}
          fromPatchset={fromPatchset}
          onFromPatchsetChange={onFromPatchsetChange}
          onToPatchsetChange={onToPatchsetChange}
          patchsets={patchsets}
          toPatchset={toPatchset}
        />
      </div>
    </section>
  );
}

function PatchsetTimeline({
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  patchsets,
  toPatchset
}: {
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  const selectablePatchsets = patchsets.filter((patchset) => patchset.id);
  const trackRef = useRef<HTMLDivElement | null>(null);
  const [dragging, setDragging] = useState<TimelineHandle | null>(null);
  const steps = useMemo<TimelineStep[]>(
    () => [
      { id: "", label: "Base", patchset: undefined },
      ...selectablePatchsets.map((patchset) => ({
        id: patchset.id || "",
        label: patchsetDotLabel(patchset),
        patchset
      }))
    ],
    [selectablePatchsets]
  );
  const fromIndex = timelineIndexForValue(steps, fromPatchset, 0);
  const toIndex = Math.max(1, timelineIndexForValue(steps, toPatchset, steps.length - 1));
  const maxIndex = Math.max(0, steps.length - 1);

  const applyIndex = (handle: TimelineHandle, index: number) => {
    if (!steps.length) {
      return;
    }

    const nextIndex =
      handle === "from"
        ? clamp(index, 0, maxIndex)
        : clamp(index, Math.min(1, maxIndex), maxIndex);
    const step = steps[nextIndex];

    if (handle === "from") {
      onFromPatchsetChange(step?.id || "");
      return;
    }

    if (step?.id) {
      onToPatchsetChange(step.id);
    }
  };

  const indexForPointer = (clientX: number) => {
    const track = trackRef.current;
    if (!track || maxIndex === 0) {
      return 0;
    }

    const rect = track.getBoundingClientRect();
    const ratio = clamp((clientX - rect.left) / rect.width, 0, 1);
    return Math.round(ratio * maxIndex);
  };

  const handlePointerDown = (
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ) => {
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(handle);
    applyIndex(handle, indexForPointer(event.clientX));
  };

  const handlePointerMove = (event: PointerEvent<HTMLButtonElement>) => {
    if (!dragging) {
      return;
    }
    applyIndex(dragging, indexForPointer(event.clientX));
  };

  const stopDragging = (event: PointerEvent<HTMLButtonElement>) => {
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragging(null);
  };

  const handleKeyDown = (
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ) => {
    if (event.key === "ArrowLeft" || event.key === "ArrowDown") {
      event.preventDefault();
      applyIndex(handle, currentIndex - 1);
    } else if (event.key === "ArrowRight" || event.key === "ArrowUp") {
      event.preventDefault();
      applyIndex(handle, currentIndex + 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      applyIndex(handle, handle === "from" ? 0 : 1);
    } else if (event.key === "End") {
      event.preventDefault();
      applyIndex(handle, maxIndex);
    }
  };

  if (!selectablePatchsets.length) {
    return (
      <p className="mt-2 rounded-md bg-slate-50 px-3 py-2 text-sm text-slate-600">
        No patchsets returned.
      </p>
    );
  }

  return (
    <div className="mt-2">
      <div className="sr-only">
        <label>
          Diff base
          <select
            aria-label="Diff base"
            onChange={(event) => onFromPatchsetChange(event.target.value)}
            value={fromPatchset}
          >
            <option value="">Recorded base</option>
            {selectablePatchsets.map((patchset) => (
              <option key={`from-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Target patchset
          <select
            aria-label="Target patchset"
            onChange={(event) => onToPatchsetChange(event.target.value)}
            value={toPatchset}
          >
            {selectablePatchsets.map((patchset) => (
              <option key={`to-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="relative h-20 px-4 md:px-5">
        <div className="relative h-full" ref={trackRef}>
          <div className="absolute inset-x-0 top-9 h-px bg-slate-300" />
          <div className="absolute inset-x-0 top-6 flex items-center justify-between">
            {steps.map((step, index) => {
              const isCurrent =
                currentPatchsetId && step.id === currentPatchsetId;
              return (
                <button
                  aria-current={index === toIndex ? "true" : undefined}
                  aria-label={
                    index === 0
                      ? "Use recorded base as diff base"
                      : `Compare to ${patchsetOptionLabel(step.patchset)}`
                  }
                  className="group flex min-w-7 -translate-y-0.5 flex-col items-center gap-1 text-[10px] font-medium text-slate-500 md:min-w-8"
                  key={step.id || "base"}
                  onClick={() =>
                    index === 0
                      ? onFromPatchsetChange("")
                      : onToPatchsetChange(step.id)
                  }
                  type="button"
                >
                  <span
                    className={cn(
                      "rounded-full border-2 bg-white transition group-hover:border-zinc-950",
                      index === fromIndex || index === toIndex
                        ? "h-3.5 w-3.5 border-zinc-950"
                        : "h-3 w-3 border-slate-300",
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "border-emerald-500 ring-2 ring-emerald-100"
                    )}
                    title={isCurrent ? "Current patchset" : undefined}
                  />
                  <span
                    className={cn(
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "text-emerald-700"
                    )}
                  >
                    {step.label}
                  </span>
                </button>
              );
            })}
          </div>

          <TimelineHandleButton
            handle="from"
            index={fromIndex}
            label={
              fromIndex === 0 ? "From Base" : `From ${steps[fromIndex]?.label}`
            }
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-0"
          />
          <TimelineHandleButton
            handle="to"
            index={toIndex}
            label={`To ${steps[toIndex]?.label || ""}`}
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-12"
          />
        </div>
      </div>
    </div>
  );
}

function TimelineHandleButton({
  handle,
  index,
  label,
  maxIndex,
  onKeyDown,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  topClassName
}: {
  handle: TimelineHandle;
  index: number;
  label: string;
  maxIndex: number;
  onKeyDown(
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ): void;
  onPointerDown(
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ): void;
  onPointerMove(event: PointerEvent<HTMLButtonElement>): void;
  onPointerUp(event: PointerEvent<HTMLButtonElement>): void;
  topClassName: string;
}) {
  return (
    <button
      aria-label={`Drag ${handle} patchset handle`}
      aria-valuemax={maxIndex}
      aria-valuemin={handle === "from" ? 0 : Math.min(1, maxIndex)}
      aria-valuenow={index}
      className={cn(
        "absolute rounded-full border px-2 py-1 text-[11px] font-semibold shadow-sm transition active:scale-[0.98]",
        handle === "from"
          ? "border-slate-300 bg-white text-slate-700"
          : "border-zinc-950 bg-zinc-950 text-white",
        topClassName
      )}
      onKeyDown={(event) => onKeyDown(handle, index, event)}
      onPointerDown={(event) => onPointerDown(handle, event)}
      onPointerCancel={onPointerUp}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      role="slider"
      style={{
        left: timelinePosition(index, maxIndex),
        touchAction: "none",
        transform: handleTransform(index, maxIndex)
      }}
      type="button"
    >
      {label}
    </button>
  );
}

function ErrorBox({
  className,
  message
}: {
  className?: string;
  message: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800",
        className
      )}
    >
      {message}
    </div>
  );
}

function PageMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="rounded-lg border border-slate-200 bg-white p-6 shadow-sm shadow-slate-200/50">
        <h1 className="text-xl font-semibold tracking-normal text-zinc-950">
          {title}
        </h1>
        <p className="mt-2 text-sm text-slate-600">{message}</p>
      </div>
    </section>
  );
}

function ChangesetSkeleton() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <div className="h-4 w-48 animate-pulse rounded bg-slate-200" />
        <div className="mt-5 h-8 w-2/3 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 flex flex-wrap gap-2">
          <div className="h-7 w-24 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-32 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-20 animate-pulse rounded bg-slate-100" />
        </div>
      </div>
    </section>
  );
}

function StatusBadge({ status }: { status?: string }) {
  const label = humanizeStatus(status);
  return (
    <span
      className={cn(
        "inline-flex rounded-md border px-2 py-0.5 text-[11px] font-semibold md:py-1 md:text-xs",
        statusClass(status)
      )}
      title={status || "unknown"}
    >
      {label}
    </span>
  );
}

// Compact one-line attribution. The authoring slice is intentionally omitted
// here because the breadcrumb already surfaces it, and the target ref is dropped
// as noise reviewers don't act on. The author links to their profile (their
// slices). On mobile this stays on a single truncated line so it never pushes
// the diff down.
function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const author = changeset.author;
  if (!author) {
    return null;
  }

  return (
    <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11px] text-slate-500 md:mt-1.5 md:text-xs">
      <span className="shrink-0">by</span>
      <Link
        className="min-w-0 truncate font-medium text-slate-700 underline-offset-2 transition hover:text-zinc-950 hover:underline"
        search={{ account: author }}
        title={`View ${author}'s slices`}
        to="/slices"
      >
        {author}
      </Link>
    </div>
  );
}

function humanizeStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  switch (normalized) {
    case "":
    case "draft":
      return "Draft";
    case "open":
      return "Open";
    case "pending_publish":
      return "Publishing";
    case "published":
      return "Published";
    case "submitted":
      return "Submitted";
    case "merged":
      return "Merged";
    case "abandoned":
      return "Abandoned";
    default:
      return status || "Unknown";
  }
}

function isPublishing(status?: string) {
  return (status || "").toLowerCase() === "pending_publish";
}

// Copies the short, shareable /cs/<id> URL to the clipboard so reviewers can
// paste it around. Falls back silently when the clipboard API is unavailable.
function CopyLinkButton({ changesetId }: { changesetId: string }) {
  const [copied, setCopied] = useState(false);
  const shareId = shortChangesetId(changesetId);

  if (!shareId) {
    return null;
  }

  const shareUrl =
    typeof window !== "undefined"
      ? `${window.location.origin}/cs/${shareId}`
      : `/cs/${shareId}`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(shareUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can be blocked (insecure origin / permissions); ignore.
    }
  };

  return (
    <button
      className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2 py-1 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950"
      onClick={copy}
      title={shareUrl}
      type="button"
    >
      <span aria-hidden="true">{copied ? "OK" : "URL"}</span>
      {copied ? "Link copied" : "Copy link"}
    </button>
  );
}

function changesetSliceSearch(changeset: Changeset) {
  const ref = changeset.authoringSlice;
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}:${ref.slice}`;
}

function changesetBreadcrumbItems({
  changeset,
  sliceSearch
}: {
  changeset: Changeset;
  sliceSearch: string;
}): Crumb[] {
  const items: Crumb[] = [{ label: "Slices", to: "/slices" }];

  if (sliceSearch) {
    const routeParams = toSliceRouteParams(changeset.authoringSlice);
    items.push(
      routeParams
        ? {
            label: sliceSearch,
            params: routeParams,
            to: "/slices/$account/$slice"
          }
        : { label: sliceSearch }
    );
    items.push({
      label: `${sliceSearch} changesets`,
      search: { slice: sliceSearch },
      to: "/changesets"
    });
  }

  items.push({ label: changesetLabel(changeset) });

  return items;
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

function shortCommit(commitId: string) {
  return shortHash(commitId);
}

function shortPatchsetId(patchsetId: string) {
  if (!patchsetId) {
    return "";
  }
  return patchsetId.replace(/^ps_/, "").slice(0, 12);
}

export function sortedPatchsets(changeset?: Changeset) {
  return [...(changeset?.patchsets || [])].sort((left, right) => {
    const leftNumber = numericPatchsetNumber(left);
    const rightNumber = numericPatchsetNumber(right);
    if (leftNumber !== rightNumber) {
      return leftNumber - rightNumber;
    }

    return patchsetKey(left).localeCompare(patchsetKey(right));
  });
}

function numericPatchsetNumber(patchset: Patchset) {
  const number = Number(patchset.number);
  return Number.isFinite(number) ? number : Number.MAX_SAFE_INTEGER;
}

function patchsetKey(patchset: Patchset) {
  return (
    patchset.id ||
    `${patchset.number || "unknown"}-${patchset.createdAt || ""}-${
      patchset.baseCommitId || patchset.basePatchsetId || ""
    }`
  );
}

function findPatchset(patchsets: Patchset[], patchsetId: string) {
  return patchsets.find((patchset) => patchset.id === patchsetId);
}

type TimelineHandle = "from" | "to";

interface TimelineStep {
  id: string;
  label: string;
  patchset?: Patchset;
}

function patchsetOptionLabel(patchset?: Patchset) {
  if (!patchset) {
    return "";
  }
  if (patchset.number !== undefined && patchset.number !== "") {
    return `Patchset ${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `Patchset ${shortId}` : "Patchset";
}

function patchsetDotLabel(patchset: Patchset) {
  if (patchset.number !== undefined && patchset.number !== "") {
    return `P${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `P${shortId}` : "P";
}

function timelineIndexForValue(
  steps: TimelineStep[],
  value: string,
  fallback: number
) {
  if (!value) {
    return 0;
  }

  const index = steps.findIndex((step) => step.id === value);
  return index >= 0 ? index : fallback;
}

function timelinePosition(index: number, maxIndex: number) {
  if (maxIndex <= 0) {
    return "0%";
  }
  return `${(index / maxIndex) * 100}%`;
}

// Shifts the handle's box at the start/end of the track so its label cannot
// overflow the container. At the left edge the box left-aligns with the dot; at
// the right edge it right-aligns; everywhere else it centers on the dot.
function handleTransform(index: number, maxIndex: number) {
  if (index <= 0) {
    return "translateX(0)";
  }
  if (maxIndex > 0 && index >= maxIndex) {
    return "translateX(-100%)";
  }
  return "translateX(-50%)";
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
    case "merged":
    case "submitted":
      return "border-emerald-200 bg-emerald-50 text-emerald-800";
    case "pending_publish":
      return "border-amber-200 bg-amber-50 text-amber-900";
    case "abandoned":
      return "border-rose-200 bg-rose-50 text-rose-800";
    case "draft":
      return "border-slate-200 bg-slate-50 text-slate-700";
    default:
      return "border-slate-200 bg-slate-50 text-slate-700";
  }
}

// A changeset that has been submitted/published/abandoned is no longer open for
// action; merge/abandon controls hide for these.
function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return (
    normalized === "submitted" ||
    normalized === "pending_publish" ||
    normalized === "published" ||
    normalized === "merged" ||
    normalized === "abandoned"
  );
}

// Submit (merge) is only valid while the changeset is still open/draft.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
