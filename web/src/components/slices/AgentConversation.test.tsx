import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { ConversationEvent } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AgentConversation } from "./AgentConversation";

type MutationOptions<TData> = {
  mutationFn: () => Promise<TData> | TData;
  onSuccess?: (data: TData) => void;
};

const queryClientMock = vi.hoisted(() => ({
  current: {
    invalidateQueries: vi.fn(),
    prefetchQuery: vi.fn(),
    setQueriesData: vi.fn(),
    setQueryData: vi.fn()
  }
}));

// Router history stub so internal markdown links can client-side navigate via
// useInternalLinkClickHandler() -> useRouter().history.push.
const routerHistoryMock = vi.hoisted(() => ({ push: vi.fn() }));

// The captured-changeset link navigates client-side via TanStack's <Link>, and
// prefetches the changeset on hover via useApi()/useQueryClient(). None of these
// have a provider in these bare component renders, so stub them. The Link stub
// resolves `$param` segments so the rendered href still matches the real route.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    to,
    params,
    children,
    ...rest
  }: {
    to: string;
    params?: Record<string, string>;
    children?: ReactNode;
  } & Record<string, unknown>) => {
    const href = to.replace(/\$([A-Za-z0-9_]+)/g, (_match, key: string) =>
      encodeURIComponent(params?.[key] ?? "")
    );
    return (
      <a href={href} {...rest}>
        {children}
      </a>
    );
  },
  useRouter: () => ({ history: routerHistoryMock })
}));

vi.mock("@tanstack/react-query", () => ({
  useMutation: <TData,>(options: MutationOptions<TData>) => ({
    isPending: false,
    mutate: vi.fn(async () => {
      const data = await options.mutationFn();
      options.onSuccess?.(data);
    })
  }),
  useQueryClient: () => queryClientMock.current
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => ({ getChangeset: vi.fn() })
}));

describe("AgentConversation", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  it("links daemon-captured changeset patchsets", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "7",
        role: "system",
        type: "status",
        text: "captured changeset abc123def0 patchset 2"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByText("Captured patchset 2")).toBeInTheDocument();
    const link = screen.getByRole("link", {
      name: "changeset abc123def0"
    });
    expect(link).toHaveAttribute("href", "/cs/abc123def0");
    await waitFor(() =>
      expect(api.streamConversation).toHaveBeenCalledWith(
        { conversationId: "conv_1", afterSeq: 0 },
        expect.any(AbortSignal)
      )
    );
  });

  it("upgrades a turn's file links in place when its patchset is captured", async () => {
    const messageEvent: ConversationEvent = {
      id: "evt_msg",
      conversationId: "conv_1",
      seq: "5",
      role: "agent",
      type: "message",
      // Before the patchset exists the server resolves the gsfile: link to the
      // slice-file view; this is what streams in first.
      text: "Edited [foo.ts](/slices/acme/backend?path=%2Ffoo.ts)."
    };
    const captureEvent: ConversationEvent = {
      id: "evt_cap",
      conversationId: "conv_1",
      seq: "6",
      role: "system",
      type: "status",
      text: "captured changeset abc123def0 patchset 1"
    };
    const upgradedMessage: ConversationEvent = {
      ...messageEvent,
      text: "Edited [foo.ts](/cs/abc123def0?to=ps_1&file=foo.ts)."
    };

    // Gate the capture event so we can observe the pre-capture (slice) link
    // before the upgrade fires.
    let releaseCapture: () => void = () => {};
    const capturePending = new Promise<void>((resolve) => {
      releaseCapture = resolve;
    });

    const getConversationEvents = vi
      .fn()
      .mockResolvedValueOnce({ events: [] }) // initial backfill: no history yet
      .mockResolvedValue({ events: [upgradedMessage, captureEvent] }); // refetch

    const api = {
      closeConversation: vi.fn(),
      sendAgentMessage: vi.fn(),
      getConversationEvents,
      streamConversation: vi.fn(async function* () {
        yield messageEvent;
        await capturePending;
        yield captureEvent;
      })
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // The message first renders pointing at the slice-file view.
    const link = await screen.findByRole("link", { name: "foo.ts" });
    expect(link).toHaveAttribute("href", "/slices/acme/backend?path=%2Ffoo.ts");

    // The patchset lands: its status event streams in and the link upgrades in
    // place to the precise changeset+patchset URL.
    releaseCapture();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "foo.ts" })).toHaveAttribute(
        "href",
        "/cs/abc123def0?to=ps_1&file=foo.ts"
      )
    );

    // The upgrade swaps text in place — it does not duplicate the message.
    expect(screen.getAllByRole("link", { name: "foo.ts" })).toHaveLength(1);
    // One backfill + exactly one capture-triggered refetch.
    expect(getConversationEvents).toHaveBeenCalledTimes(2);
  });

  it("rehydrates live deltas after capture when no final message arrives", async () => {
    const deltaEvent: ConversationEvent = {
      id: "evt_delta",
      conversationId: "conv_1",
      seq: "5",
      role: "agent",
      type: "message_delta",
      text: "Edited [foo.ts](/slices/acme/backend?path=%2Ffoo.ts).",
      itemId: "msg_1"
    };
    const captureEvent: ConversationEvent = {
      id: "evt_cap",
      conversationId: "conv_1",
      seq: "6",
      role: "system",
      type: "status",
      text: "captured changeset abc123def0 patchset 1"
    };
    const upgradedDelta: ConversationEvent = {
      ...deltaEvent,
      text: "Edited [foo.ts](/cs/abc123def0?to=ps_1&file=foo.ts)."
    };

    let releaseCapture: () => void = () => {};
    const capturePending = new Promise<void>((resolve) => {
      releaseCapture = resolve;
    });

    const getConversationEvents = vi
      .fn()
      .mockResolvedValueOnce({ events: [] })
      .mockResolvedValue({ events: [upgradedDelta, captureEvent] });

    const api = {
      closeConversation: vi.fn(),
      sendAgentMessage: vi.fn(),
      getConversationEvents,
      streamConversation: vi.fn(async function* () {
        yield deltaEvent;
        await capturePending;
        yield captureEvent;
      })
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByRole("link", { name: "foo.ts" })).toHaveAttribute(
      "href",
      "/slices/acme/backend?path=%2Ffoo.ts"
    );

    releaseCapture();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "foo.ts" })).toHaveAttribute(
        "href",
        "/cs/abc123def0?to=ps_1&file=foo.ts"
      )
    );

    expect(screen.getAllByRole("link", { name: "foo.ts" })).toHaveLength(1);
    expect(screen.queryByText("message_delta")).not.toBeInTheDocument();
    expect(screen.queryByText("Edited", { exact: false })).toBeInTheDocument();
    expect(getConversationEvents).toHaveBeenCalledTimes(2);
  });

  it("backfills persisted history in one batch and tails the stream", async () => {
    const persisted: ConversationEvent[] = [
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "first question"
      },
      {
        id: "evt_2",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "message",
        text: "first answer"
      }
    ];
    const api = {
      sendAgentMessage: vi.fn(),
      getConversationEvents: vi.fn(async () => ({ events: persisted })),
      // The stream only carries the live tail here; the batch already rendered
      // the history, so the stream must open after the last persisted seq.
      streamConversation: vi.fn(async function* () {})
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByText("first answer")).toBeInTheDocument();
    expect(screen.getByText("first question")).toBeInTheDocument();
    expect(api.getConversationEvents).toHaveBeenCalledWith({
      conversationId: "conv_1",
      afterSeq: 0
    });
    // Tailing from the last persisted seq, not replaying from 0.
    await waitFor(() =>
      expect(api.streamConversation).toHaveBeenCalledWith(
        { conversationId: "conv_1", afterSeq: 2 },
        expect.any(AbortSignal)
      )
    );
  });

  it("renders agent messages as markdown", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "agent",
        type: "message",
        text: "Here is **bold** text and a [link](https://example.com).\n\n- one\n- two"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // Inline emphasis and lists become real elements rather than literal markup.
    const strong = await screen.findByText("bold");
    expect(strong.tagName).toBe("STRONG");
    const link = screen.getByRole("link", { name: "link" });
    expect(link).toHaveAttribute("href", "https://example.com");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByText("one").tagName).toBe("LI");
  });

  it("routes internal markdown links client-side and opens external ones in a new tab", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "agent",
        type: "message",
        text: "Open [the changeset](/cs/abc123?to=ps_1&file=foo.ts) or [docs](https://example.com)."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    const internal = await screen.findByRole("link", { name: "the changeset" });
    const external = screen.getByRole("link", { name: "docs" });

    // External links open in a new tab; internal app links stay in-page so they
    // can be intercepted for client-side routing.
    expect(external).toHaveAttribute("target", "_blank");
    expect(internal).not.toHaveAttribute("target");

    // A plain left-click on an internal link navigates via the router instead of
    // triggering a full page load.
    fireEvent.click(internal);
    expect(routerHistoryMock.push).toHaveBeenCalledWith(
      "/cs/abc123?to=ps_1&file=foo.ts"
    );
    expect(routerHistoryMock.push).toHaveBeenCalledTimes(1);
  });

  it("keeps user messages as literal text, not markdown", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "use **stars** literally"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // The user's asterisks are preserved verbatim instead of becoming <strong>.
    expect(
      await screen.findByText("use **stars** literally")
    ).toBeInTheDocument();
  });

  it("renders read-only transcripts without the message composer", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "agent",
        type: "message",
        text: "Shared transcript"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
        readOnly
      />
    );

    expect(await screen.findByText("Shared transcript")).toBeInTheDocument();
    expect(
      screen.queryByRole("textbox", { name: /message/i })
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Send" })
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Close" })
    ).not.toBeInTheDocument();
  });

  it("closes active conversations after confirmation and updates cached queries", async () => {
    const api = makeApi([]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work", status: "active" }}
        conversationId="conv_1"
      />
    );

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    const dialog = screen.getByRole("dialog", {
      name: "Close conversation"
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));

    await waitFor(() =>
      expect(api.closeConversation).toHaveBeenCalledWith({
        conversationId: "conv_1"
      })
    );
    expect(queryClientMock.current.setQueryData).toHaveBeenCalledWith(
      ["conversation", "conv_1"],
      expect.objectContaining({ id: "conv_1", status: "inactive" })
    );
    expect(queryClientMock.current.setQueriesData).toHaveBeenCalledWith(
      { queryKey: ["sliceConversations"] },
      expect.any(Function)
    );
    expect(queryClientMock.current.invalidateQueries).toHaveBeenCalledWith({
      queryKey: ["sliceConversations"]
    });
  });

  it("does not close active conversations when the close dialog is cancelled", async () => {
    const api = makeApi([]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work", status: "active" }}
        conversationId="conv_1"
      />
    );

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    const dialog = screen.getByRole("dialog", {
      name: "Close conversation"
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(api.closeConversation).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("dialog", { name: "Close conversation" })
    ).not.toBeInTheDocument();
  });

  it("collapses trace and tool events separately from messages", async () => {
    const api = makeApi([
      {
        id: "evt_1",
        conversationId: "conv_1",
        seq: "1",
        role: "agent",
        type: "delta",
        text: "thinking about the workspace"
      },
      {
        id: "evt_2",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "tool_call",
        text: "rg agent"
      },
      {
        id: "evt_3",
        conversationId: "conv_1",
        seq: "3",
        role: "agent",
        type: "message",
        text: "Done."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    const summary = await screen.findByText("Trace and tools (2)");
    const details = summary.closest("details");
    expect(details).not.toHaveAttribute("open");
    expect(screen.getByText("Done.")).toBeVisible();

    fireEvent.click(summary);
    expect(details).toHaveAttribute("open");
  });

  it("coalesces streamed token deltas and replaces them with the final", async () => {
    const api = makeApi([
      {
        conversationId: "conv_1",
        role: "agent",
        type: "message_delta",
        text: "Hel",
        itemId: "msg_1"
      },
      {
        conversationId: "conv_1",
        role: "agent",
        type: "message_delta",
        text: "Hello wor",
        itemId: "msg_1"
      },
      {
        id: "evt_final",
        conversationId: "conv_1",
        seq: "4",
        role: "agent",
        type: "message",
        text: "Hello world",
        itemId: "msg_1"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // The persisted final supersedes the in-progress delta: only one bubble
    // with the final text remains, and the interim text is gone.
    expect(await screen.findByText("Hello world")).toBeInTheDocument();
    expect(screen.queryByText("Hello wor")).not.toBeInTheDocument();
  });

  it("coalesces persisted message deltas during history backfill", async () => {
    const api = {
      sendAgentMessage: vi.fn(),
      getConversationEvents: vi.fn(async () => ({
        events: [
          {
            id: "evt_delta_1",
            conversationId: "conv_1",
            seq: "1",
            role: "agent",
            type: "message_delta",
            text: "Hel",
            itemId: "msg_1"
          },
          {
            id: "evt_delta_2",
            conversationId: "conv_1",
            seq: "2",
            role: "agent",
            type: "message_delta",
            text: "Hello wor",
            itemId: "msg_1"
          },
          {
            id: "evt_final",
            conversationId: "conv_1",
            seq: "3",
            role: "agent",
            type: "message",
            text: "Hello world",
            itemId: "msg_1"
          }
        ]
      })),
      streamConversation: vi.fn(async function* () {})
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByText("Hello world")).toBeInTheDocument();
    expect(screen.queryByText("Hello wor")).not.toBeInTheDocument();
    expect(screen.queryByText("message_delta")).not.toBeInTheDocument();
  });

  it("collapses persisted thinking deltas with nearby trace events", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "inspect the workspace"
      },
      {
        id: "evt_thinking",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "reasoning_delta",
        text: "checking the file tree",
        itemId: "think_1"
      },
      {
        id: "evt_tool",
        conversationId: "conv_1",
        seq: "3",
        role: "agent",
        type: "tool_call",
        text: "rg agent"
      },
      {
        id: "evt_done",
        conversationId: "conv_1",
        seq: "4",
        role: "agent",
        type: "message",
        text: "Done."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    const traceSummary = await screen.findByText("Trace and tools (2)");
    const traceDetails = traceSummary.closest("details");
    const done = screen.getByText("Done.");

    expect(traceDetails).not.toHaveAttribute("open");
    expect(screen.queryByText("Thinking")).not.toBeInTheDocument();
    expectElementBefore(traceSummary, done);

    fireEvent.click(traceSummary);
    expect(traceDetails).toHaveAttribute("open");
    expect(traceDetails).toHaveTextContent("checking the file tree");
    expect(traceDetails).toHaveTextContent("rg agent");
  });

  it("coalesces cumulative reasoning snapshots into a single trace entry", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "ok"
      },
      {
        id: "evt_think_1",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "reasoning_delta",
        text: "The",
        itemId: "think_1"
      },
      {
        id: "evt_think_2",
        conversationId: "conv_1",
        seq: "3",
        role: "agent",
        type: "reasoning_delta",
        text: "The user said",
        itemId: "think_1"
      },
      {
        id: "evt_think_3",
        conversationId: "conv_1",
        seq: "4",
        role: "agent",
        type: "reasoning_delta",
        text: "The user said ok, which",
        itemId: "think_1"
      },
      {
        id: "evt_done",
        conversationId: "conv_1",
        seq: "5",
        role: "agent",
        type: "message",
        text: "Done."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // The three growing-prefix snapshots collapse to one trace entry, not three.
    await screen.findByText("Agent trace (1)");
    // Only the latest snapshot survives.
    const latestTrace = await screen.findByText("The user said ok, which", {
      selector: "pre"
    });
    const traceDetails = latestTrace.closest("details");
    expect(traceDetails).not.toBeNull();
    expect(traceDetails).not.toHaveAttribute("open");
    const traceSummary = traceDetails?.querySelector("summary");
    expect(traceSummary).not.toBeNull();
    fireEvent.click(traceSummary as HTMLElement);
    expect(traceDetails).toHaveAttribute("open");
    expect(traceDetails?.querySelectorAll("pre").length ?? 0).toBe(1);
  });

  it("lets the finalized reasoning event supersede its snapshots", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "ok"
      },
      {
        id: "evt_think_1",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "reasoning_delta",
        text: "The user said",
        itemId: "think_1"
      },
      {
        id: "evt_think_final",
        conversationId: "conv_1",
        seq: "3",
        role: "agent",
        type: "reasoning",
        text: "The user said ok, which means continue.",
        itemId: "think_1"
      },
      {
        id: "evt_done",
        conversationId: "conv_1",
        seq: "4",
        role: "agent",
        type: "message",
        text: "Done."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    await screen.findByText("Agent trace (1)");
    const finalizedTrace = await screen.findByText(
      "The user said ok, which means continue.",
      { selector: "pre" }
    );
    const traceDetails = finalizedTrace.closest("details");
    const traceSummary = traceDetails?.querySelector("summary");
    expect(traceSummary).not.toBeNull();
    fireEvent.click(traceSummary as HTMLElement);
    expect(traceDetails?.querySelectorAll("pre").length ?? 0).toBe(1);
  });

  it("shows a working indicator while the agent is mid-turn", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "inspect the workspace"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    const status = await screen.findByRole("status", { name: "Agent activity" });
    expect(status).toHaveTextContent("Agent is working…");
  });

  it("reads the indicator as thinking while reasoning streams", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "inspect the workspace"
      },
      {
        id: "evt_think",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "reasoning_delta",
        text: "checking the file tree",
        itemId: "think_1"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    const status = await screen.findByRole("status", { name: "Agent activity" });
    expect(status).toHaveTextContent("Agent is thinking…");
  });

  it("clears the working indicator once the turn completes", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "inspect the workspace"
      },
      {
        id: "evt_done",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "message",
        text: "Done."
      },
      {
        id: "evt_turn_complete",
        conversationId: "conv_1",
        seq: "3",
        role: "system",
        type: "turn_complete"
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByText("Done.")).toBeInTheDocument();
    expect(
      screen.queryByRole("status", { name: "Agent activity" })
    ).not.toBeInTheDocument();
    // The turn_complete marker is a control event, never shown in the transcript.
    expect(screen.queryByText("turn_complete")).not.toBeInTheDocument();
  });

  it("keeps the working indicator after an agent message until the turn completes", async () => {
    const api = makeApi([
      {
        id: "evt_user",
        conversationId: "conv_1",
        seq: "1",
        role: "user",
        type: "message",
        text: "inspect the workspace"
      },
      {
        id: "evt_msg",
        conversationId: "conv_1",
        seq: "2",
        role: "agent",
        type: "message",
        text: "Looking into it now."
      }
    ]);

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // The agent has sent a message but no turn_complete marker yet, so the turn
    // is still in flight and the indicator must remain visible.
    expect(await screen.findByText("Looking into it now.")).toBeInTheDocument();
    expect(
      await screen.findByRole("status", { name: "Agent activity" })
    ).toBeInTheDocument();
  });

  it("shows stream failures in the header and auto-reconnects", async () => {
    let attempts = 0;
    const api = {
      sendAgentMessage: vi.fn(),
      getConversationEvents: vi.fn(async () => ({ events: [] })),
      streamConversation: vi.fn(async function* (
        _request: { conversationId: string; afterSeq: number },
        signal: AbortSignal
      ) {
        attempts += 1;
        if (attempts === 1) {
          // Yield one event so the user can see the partial transcript, then
          // blow up to simulate the server hanging up.
          yield {
            id: "evt_1",
            conversationId: "conv_1",
            seq: "1",
            role: "agent",
            type: "message",
            text: "partial"
          };
          if (!signal.aborted) {
            throw new Error("stream exploded");
          }
          return;
        }
        yield {
          id: "evt_2",
          conversationId: "conv_1",
          seq: "2",
          role: "agent",
          type: "message",
          text: "reconnected"
        };
      })
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    expect(await screen.findByText("partial")).toBeInTheDocument();
    const status = await screen.findByRole("status", {
      name: "Conversation stream status"
    });
    expect(status).toHaveTextContent("Stream stopped; reconnecting");
    expect(status).toHaveTextContent("stream exploded");

    expect(
      await screen.findByText("reconnected", {}, { timeout: 3000 })
    ).toBeInTheDocument();
    // The retry must have triggered at least one additional stream call after
    // the initial failure. We don't assert exact count - StrictMode or React
    // 18's effect re-runs can add extra attempts, but the user-visible
    // contract is "the header reports the stop, then reconnects itself".
    expect(vi.mocked(api.streamConversation).mock.calls.length).toBeGreaterThan(1);
  });

  it("preserves an in-progress draft across auto-reconnect", async () => {
    let attempts = 0;
    const api = {
      sendAgentMessage: vi.fn(),
      getConversationEvents: vi.fn(async () => ({ events: [] })),
      streamConversation: vi.fn(async function* (
        _request: { conversationId: string; afterSeq: number },
        signal: AbortSignal
      ) {
        attempts += 1;
        if (attempts === 1) {
          if (!signal.aborted) {
            throw new Error("stream exploded");
          }
          return;
        }
        // Second attempt: keep the stream open (no events) so the draft the
        // user typed before reconnecting is the only thing under test.
      })
    } as unknown as ApiClient;

    render(
      <AgentConversation
        api={api}
        conversation={{ id: "conv_1", title: "Agent work" }}
        conversationId="conv_1"
      />
    );

    // Type a draft while the stream is errored, then let the automatic retry run.
    await screen.findByRole("status", {
      name: "Conversation stream status"
    });
    const textarea = screen.getByPlaceholderText(
      "Ask the agent to inspect or edit this slice"
    );
    fireEvent.change(textarea, { target: { value: "half-written prompt" } });

    // The reconnect must not wipe the user's unsent text.
    await waitFor(
      () =>
        expect(vi.mocked(api.streamConversation).mock.calls.length).toBeGreaterThan(1),
      { timeout: 3000 }
    );
    expect(textarea).toHaveValue("half-written prompt");
  });
});

function makeApi(events: ConversationEvent[]) {
  return {
    closeConversation: vi.fn(async () => ({
      id: "conv_1",
      status: "inactive"
    })),
    sendAgentMessage: vi.fn(),
    // Backfill returns no persisted history here, so the transcript still
    // arrives through the stream — these tests exercise the streaming/live-delta
    // path. The batch backfill path has its own test below.
    getConversationEvents: vi.fn(async () => ({ events: [] })),
    streamConversation: vi.fn(async function* () {
      for (const event of events) {
        yield event;
      }
    })
  } as unknown as ApiClient;
}

function expectElementBefore(left: Element, right: Element) {
  expect(
    left.compareDocumentPosition(right) & Node.DOCUMENT_POSITION_FOLLOWING
  ).not.toBe(0);
}
