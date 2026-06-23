import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ConversationEvent } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AgentConversation } from "./AgentConversation";

describe("AgentConversation", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.clearAllMocks();
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
    const traceSummary = await screen.findByText("Agent trace (1)");
    const traceDetails = traceSummary.closest("details");
    expect(traceDetails).not.toBeNull();
    fireEvent.click(traceSummary);
    // Only the latest snapshot survives.
    expect(traceDetails).toHaveTextContent("The user said ok, which");
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

    const traceSummary = await screen.findByText("Agent trace (1)");
    const traceDetails = traceSummary.closest("details");
    fireEvent.click(traceSummary);
    expect(traceDetails).toHaveTextContent(
      "The user said ok, which means continue."
    );
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

  it("clears the working indicator once the agent replies", async () => {
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
  });

  it("shows stream failures in the header and auto-reconnects", async () => {
    let attempts = 0;
    const api = {
      sendAgentMessage: vi.fn(),
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
    sendAgentMessage: vi.fn(),
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
