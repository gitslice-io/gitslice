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

  it("shows a Reconnect button and re-attaches the stream after an error", async () => {
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
    const reconnect = await screen.findByRole("button", { name: "Reconnect" });
    fireEvent.click(reconnect);

    expect(await screen.findByText("reconnected")).toBeInTheDocument();
    // The retry must have triggered at least one additional stream call after
    // the initial failure. We don't assert exact count — StrictMode or React
    // 18's effect re-runs can add extra attempts, but the user-visible
    // contract is "click Reconnect, get fresh events".
    expect(vi.mocked(api.streamConversation).mock.calls.length).toBeGreaterThan(1);
  });

  it("preserves an in-progress draft across a Reconnect", async () => {
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

    // Type a draft while the stream is errored, then reconnect.
    const reconnect = await screen.findByRole("button", { name: "Reconnect" });
    const textarea = screen.getByPlaceholderText(
      "Ask the agent to inspect or edit this slice"
    );
    fireEvent.change(textarea, { target: { value: "half-written prompt" } });
    fireEvent.click(reconnect);

    // The reconnect must not wipe the user's unsent text.
    await waitFor(() =>
      expect(vi.mocked(api.streamConversation).mock.calls.length).toBeGreaterThan(1)
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
