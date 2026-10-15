import type { Conversation, ConversationEvent } from "../../api/types";

// Control event the daemon emits when an agent turn ends. It is not part of the
// visible transcript; it only closes the "agent is working" indicator. A turn
// can emit several finalized messages before it finishes, so this marker — not a
// finalized message — is the reliable end-of-turn signal.
const TURN_COMPLETE_TYPE = "turn_complete";

export function updateConversationList(
  current: Conversation[] | undefined,
  conversationId: string,
  updatedConversation: Conversation
) {
  if (!current) {
    return current;
  }
  return current.map((item) =>
    item.id === conversationId || item.id === updatedConversation.id
      ? { ...item, ...updatedConversation }
      : item
  );
}

type ConversationItem =
  | { kind: "event"; event: ConversationEvent }
  | { kind: "trace"; events: ConversationEvent[] }
  | { kind: "live"; entry: LiveDeltaEntry };

export interface LiveDeltaEntry {
  afterSeq: number;
  event: ConversationEvent;
  order: number;
}

export function groupConversationEvents(
  events: ConversationEvent[],
  liveDeltas: LiveDeltaEntry[]
): ConversationItem[] {
  const items: ConversationItem[] = [];
  let traceEvents: ConversationEvent[] = [];
  const liveEntries = [...liveDeltas].sort(compareLiveDeltaEntries);
  let liveIndex = 0;

  function flushTraceEvents() {
    if (traceEvents.length) {
      items.push({ kind: "trace", events: traceEvents });
      traceEvents = [];
    }
  }

  function flushLiveDeltasBefore(seq: number) {
    while (
      liveIndex < liveEntries.length &&
      liveEntries[liveIndex].afterSeq < seq
    ) {
      flushTraceEvents();
      items.push({ kind: "live", entry: liveEntries[liveIndex] });
      liveIndex += 1;
    }
  }

  for (const event of events) {
    // turn_complete is a control marker (it closes the working indicator), not
    // visible transcript content.
    if ((event.type ?? "").toLowerCase() === TURN_COMPLETE_TYPE) {
      continue;
    }
    const seq = eventSequenceNumber(event);
    if (seq !== undefined) {
      flushLiveDeltasBefore(seq);
    }

    if (isTraceEvent(event)) {
      traceEvents.push(event);
      continue;
    }

    flushTraceEvents();
    items.push({ kind: "event", event });
  }

  flushTraceEvents();
  while (liveIndex < liveEntries.length) {
    items.push({ kind: "live", entry: liveEntries[liveIndex] });
    liveIndex += 1;
  }
  return items;
}

// isLiveDelta detects an in-progress assistant message stream. Reasoning and
// tool trace events stay in the normal event list so they render in the
// collapsed trace groups by default.
export function isLiveDelta(event: ConversationEvent): event is ConversationEvent & {
  itemId: string;
} {
  return Boolean(event.itemId) && event.type === "message_delta";
}

// agentTurnLabel reports what the agent appears to be doing while the user is
// waiting on a reply, or null when the agent is idle. The turn is "in flight"
// from the user's most recent message until a finalized agent message, an
// error, or a status event (cancellation / patchset capture) closes it. An
// actively streaming assistant message is its own indicator — the
// LiveDeltaBubble shows an animated cursor — so we return null in that case to
// avoid a redundant second affordance.
export function agentTurnLabel(
  events: ConversationEvent[],
  liveDeltas: Map<string, LiveDeltaEntry>
): string | null {
  let lastUserSeq = 0;
  for (const event of events) {
    if ((event.role ?? "").toLowerCase() === "user") {
      const seq = eventSequenceNumber(event);
      if (seq !== undefined && seq > lastUserSeq) {
        lastUserSeq = seq;
      }
    }
  }
  if (lastUserSeq === 0 || liveDeltas.size > 0) {
    return null;
  }

  let label = "Agent is working…";
  for (const event of events) {
    const seq = eventSequenceNumber(event);
    if (seq === undefined || seq <= lastUserSeq) {
      continue;
    }
    const role = (event.role ?? "").toLowerCase();
    const type = (event.type ?? "").toLowerCase();
    // The explicit turn_complete marker, an error, or a status event (e.g.
    // "canceled" or a captured patchset) marks the end of the turn. A finalized
    // agent message does NOT: a turn can emit several messages and keep working
    // (more reasoning / tool calls) before it finishes, so the indicator must
    // stay up until turn_complete.
    if (
      type === TURN_COMPLETE_TYPE ||
      type === "error" ||
      type === "status"
    ) {
      return null;
    }
    // Otherwise the most recent activity hints at what it's doing now.
    if (type === "reasoning" || type === "reasoning_delta") {
      label = "Agent is thinking…";
    } else if (
      role === "tool" ||
      type === "tool_call" ||
      type === "tool_output"
    ) {
      label = "Agent is working…";
    }
  }

  return label;
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
    type === "reasoning_delta"
  );
}

export function traceGroupLabel(events: ConversationEvent[]) {
  const hasToolEvent = events.some((event) => {
    const type = (event.type ?? "").toLowerCase();
    return type === "tool_call" || type === "tool_output";
  });
  const hasTraceEvent = events.some((event) => {
    const type = (event.type ?? "").toLowerCase();
    return (
      type === "delta" ||
      type === "thinking" ||
      type === "thinking_trace" ||
      type === "reasoning" ||
      type === "reasoning_delta"
    );
  });

  if (hasToolEvent && hasTraceEvent) {
    return `Trace and tools (${events.length})`;
  }
  if (hasToolEvent) {
    return `Tool activity (${events.length})`;
  }
  return `Agent trace (${events.length})`;
}

export function tracePreview(events: ConversationEvent[]) {
  const firstContent = events
    .map(traceEventContent)
    .find((content) => content.trim() !== "");
  if (!firstContent) {
    return "";
  }
  return firstContent.trim().split(/\r?\n/, 1)[0];
}

export function traceEventLabel(event: ConversationEvent) {
  const role = event.role || "agent";
  const type = event.type || "trace";
  return `${role} ${type}`;
}

export function traceEventContent(event: ConversationEvent) {
  const text = event.text ?? "";
  const data = event.dataJson ?? "";
  // Tool events carry a short label in `text` (e.g. the command) and the full
  // structured payload in `dataJson` (e.g. captured output). Show both so the
  // trace is actually inspectable, but avoid duplicating when they're equal.
  if (text && data && text !== data) {
    return `${text}\n${data}`;
  }
  return text || data;
}

export function traceGroupKey(events: ConversationEvent[], index: number) {
  const first = events[0] ? eventIdentity(events[0]) : "";
  const last = events[events.length - 1]
    ? eventIdentity(events[events.length - 1])
    : "";
  return `trace:${first || index}:${last || events.length}`;
}

export function appendConversationEvent(
  current: ConversationEvent[],
  event: ConversationEvent
) {
  const identity = eventIdentity(event);
  if (identity && current.some((item) => eventIdentity(item) === identity)) {
    return current;
  }

  // Persisted *_delta events carry full cumulative snapshots, so keeping every
  // one stacks the transcript with growing-prefix duplicates. Coalesce by item:
  // a newer snapshot replaces the previous snapshot, and the finalized item
  // removes its snapshots.
  let base = current;
  if (event.itemId) {
    if (isMessageDelta(event)) {
      if (
        current.some(
          (item) => item.itemId === event.itemId && isFinalMessageEvent(item)
        )
      ) {
        return current;
      }
      base = current.filter(
        (item) => !(item.itemId === event.itemId && isMessageDelta(item))
      );
    } else if (isFinalMessageEvent(event)) {
      base = current.filter(
        (item) => !(item.itemId === event.itemId && isMessageDelta(item))
      );
    } else if (isReasoningEvent(event)) {
      base = current.filter(
        (item) => !(item.itemId === event.itemId && isReasoningDelta(item))
      );
    }
  }

  const next = [...base, event];
  if (hasSequence(event)) {
    next.sort(compareConversationEvents);
  }
  return next;
}

export function coalesceConversationEvents(events: ConversationEvent[]) {
  return events.reduce<ConversationEvent[]>(appendConversationEvent, []);
}

function isReasoningDelta(event: ConversationEvent) {
  return (event.type ?? "").toLowerCase() === "reasoning_delta";
}

export function isMessageDelta(event: ConversationEvent) {
  return (event.type ?? "").toLowerCase() === "message_delta";
}

function isFinalMessageEvent(event: ConversationEvent) {
  return (event.type ?? "").toLowerCase() === "message";
}

function isReasoningEvent(event: ConversationEvent) {
  const type = (event.type ?? "").toLowerCase();
  return type === "reasoning_delta" || type === "reasoning";
}

function compareConversationEvents(
  left: ConversationEvent,
  right: ConversationEvent
) {
  const leftSeq = eventSequenceNumber(left);
  const rightSeq = eventSequenceNumber(right);
  if (leftSeq !== undefined && rightSeq !== undefined) {
    return leftSeq - rightSeq;
  }
  return 0;
}

function compareLiveDeltaEntries(left: LiveDeltaEntry, right: LiveDeltaEntry) {
  if (left.afterSeq !== right.afterSeq) {
    return left.afterSeq - right.afterSeq;
  }
  return left.order - right.order;
}

export function rememberPersistedEventSeq(
  event: ConversationEvent,
  latestPersistedSeqRef: { current: number }
) {
  const seq = eventSequenceNumber(event);
  if (seq !== undefined && seq > latestPersistedSeqRef.current) {
    latestPersistedSeqRef.current = seq;
  }
}

function eventSequenceNumber(event: ConversationEvent) {
  const seq = Number(event.seq);
  return Number.isFinite(seq) && seq > 0 ? seq : undefined;
}

// oldestSeqOf returns the smallest positive seq among the given events, or 0
// when none carry a seq. Used to advance the reverse-pagination cursor to the
// oldest event currently held.
export function oldestSeqOf(events: ConversationEvent[]) {
  let oldest = 0;
  for (const event of events) {
    const seq = eventSequenceNumber(event);
    if (seq !== undefined && (oldest === 0 || seq < oldest)) {
      oldest = seq;
    }
  }
  return oldest;
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

export function eventKey(event: ConversationEvent, index: number) {
  return eventIdentity(event) || `${event.role ?? "event"}-${index}`;
}

export function hasSequence(event: ConversationEvent) {
  return event.seq !== undefined && event.seq !== "";
}

// isPatchsetStatusEvent reports whether an event is the daemon's end-of-turn
// "captured/created/updated … patchset N" status marker. The daemon persists the
// patchset (stamping it with the turn's conversation seq) before emitting this,
// so its arrival is the moment the server can resolve this turn's gsfile: links
// to the changeset+patchset — see refreshResolvedLinks in the stream effect.
export function isPatchsetStatusEvent(event: ConversationEvent) {
  return (
    event.role === "system" &&
    event.type === "status" &&
    /\bpatchset\b/i.test(event.text ?? "")
  );
}

export function parseCapturedPatchset(event: ConversationEvent) {
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
