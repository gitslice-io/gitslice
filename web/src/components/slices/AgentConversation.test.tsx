import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
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
