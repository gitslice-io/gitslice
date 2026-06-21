import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within
} from "@testing-library/react";
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

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations"
    });
    expect(within(sidebar).getByText("Codex laptop")).toBeInTheDocument();
    expect(
      within(sidebar).getByRole("button", { name: "New conversation" })
    ).toBeInTheDocument();
    expect(within(sidebar).getByText("No conversations yet."))
      .toBeInTheDocument();
    await waitFor(() =>
      expect(api.listConversations).toHaveBeenCalledWith({
        slice: { account: "nic", slice: "home" }
      })
    );
  });

  it("toggles the conversation sidebar and opens the create form there", async () => {
    const api = makeApi();

    render(<AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />);

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations"
    });
    fireEvent.click(
      within(sidebar).getByRole("button", { name: "New conversation" })
    );

    expect(within(sidebar).getByLabelText("Agent daemon"))
      .toHaveValue("daemon_1");
    expect(within(sidebar).getByRole("button", { name: "Create conversation" }))
      .toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Hide conversations" }));
    expect(
      screen.queryByRole("complementary", { name: "Agent conversations" })
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Show conversations" }));
    expect(
      screen.getByRole("complementary", { name: "Agent conversations" })
    ).toBeInTheDocument();
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
