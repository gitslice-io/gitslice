import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { AgentsTab } from "./AgentsTab";

describe("AgentsTab", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders online daemons and the empty conversation state", async () => {
    const api = makeApi();

    render(<AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />);

    expect(await screen.findAllByText("Codex laptop")).toHaveLength(2);
    expect(screen.getByText("No conversations yet.")).toBeInTheDocument();
    await waitFor(() =>
      expect(api.listConversations).toHaveBeenCalledWith({
        slice: { account: "nic", slice: "home" }
      })
    );
  });

  it("selects the newest conversation first", async () => {
    const api = makeApi({
      conversations: [
        {
          id: "conv_old",
          title: "Old chat",
          status: "active",
          createdAt: "2026-06-21T16:00:00Z"
        },
        {
          id: "conv_new",
          title: "Fresh chat",
          status: "active",
          createdAt: "2026-06-21T16:45:00Z"
        }
      ]
    });

    render(<AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />);

    expect(await screen.findByRole("heading", { name: "Fresh chat" }))
      .toBeInTheDocument();
  });
});

function makeApi(
  overrides: {
    conversations?: Array<{
      id: string;
      title: string;
      status: string;
      createdAt: string;
    }>;
  } = {}
) {
  return {
    listDaemons: vi.fn().mockResolvedValue({
      daemons: [
        {
          id: "daemon_1",
          account: "nic",
          name: "Codex laptop",
          runtime: "codex",
          version: "1.0.0",
          status: "online",
          lastSeenAt: "2026-06-21T00:00:00Z",
          createdAt: "2026-06-21T00:00:00Z"
        }
      ]
    }),
    listConversations: vi.fn().mockResolvedValue({
      conversations: overrides.conversations ?? []
    }),
    createConversation: vi.fn(),
    getConversation: vi.fn(),
    sendAgentMessage: vi.fn(),
    streamConversation: vi.fn(async function* () {})
  } as unknown as ApiClient;
}
