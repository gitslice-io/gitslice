import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Conversation } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AgentsTab } from "./AgentsTab";

describe("AgentsTab", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders online daemons and the empty conversation state", async () => {
    const api = makeApi();

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations",
    });
    expect(within(sidebar).getByText("Codex laptop")).toBeInTheDocument();
    expect(
      within(sidebar).getByRole("button", { name: "New conversation" }),
    ).toBeInTheDocument();
    expect(
      within(sidebar).getByText("No conversations yet."),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(api.listConversations).toHaveBeenCalledWith({
        slice: { account: "nic", slice: "home" },
      }),
    );
  });

  it("toggles the conversation sidebar and opens the create form in a dialog", async () => {
    const api = makeApi();

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations",
    });
    fireEvent.click(
      within(sidebar).getByRole("button", { name: "New conversation" }),
    );

    const dialog = await screen.findByRole("dialog", {
      name: "New conversation",
    });
    expect(within(dialog).getByLabelText("Agent daemon")).toHaveValue(
      "daemon_1",
    );
    expect(
      within(dialog).getByRole("button", { name: "Create conversation" }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Hide conversations" }));
    expect(
      screen.getByRole("button", { name: "Show conversations" }),
    ).toBeInTheDocument();
    expect(sidebar.className).toContain("lg:hidden");

    fireEvent.click(screen.getByRole("button", { name: "Show conversations" }));
    expect(
      screen.getByRole("complementary", { name: "Agent conversations" }),
    ).toBeInTheDocument();
  });

  it("selects the newest conversation first", async () => {
    const api = makeApi({
      conversations: [
        {
          id: "conv_old",
          title: "Old chat",
          status: "active",
          createdAt: "2026-06-21T16:00:00Z",
        },
        {
          id: "conv_new",
          title: "Fresh chat",
          status: "active",
          createdAt: "2026-06-21T16:45:00Z",
        },
      ],
    });

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    expect(
      await screen.findByRole("heading", { name: "Fresh chat" }),
    ).toBeInTheDocument();
  });

  it("renders a status pill with aria-current on the selected conversation", async () => {
    const api = makeApi({
      conversations: [
        {
          id: "conv_one",
          title: "Only chat",
          status: "active",
          createdAt: "2026-06-21T16:00:00Z",
        },
      ],
    });

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    // The button's accessible name combines title + status, so match loosely.
    const selected = await screen.findByRole("button", { name: /Only chat/ });
    expect(selected).toHaveAttribute("aria-current", "true");
    // Status text is rendered inside the button as an uppercase pill.
    expect(within(selected).getByText("active")).toBeInTheDocument();
  });

  it("optimistically prepends a newly created conversation", async () => {
    const created: Conversation = {
      id: "conv_new",
      title: "Freshly minted",
      status: "active",
    };
    const api = makeApi({ onCreate: () => created });

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations",
    });
    fireEvent.click(
      within(sidebar).getByRole("button", { name: "New conversation" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "New conversation",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create conversation" }),
    );

    expect(
      await screen.findByRole("heading", { name: "Freshly minted" }),
    ).toBeInTheDocument();
    expect(api.createConversation).toHaveBeenCalledWith({
      daemonId: "daemon_1",
      slice: { account: "nic", slice: "home" },
      title: undefined,
    });
  });

  it("opens the create dialog from the empty-state shortcut", async () => {
    const api = makeApi();

    renderWithClient(
      <AgentsTab api={api} slice={{ account: "nic", slice: "home" }} />,
    );

    // Wait for daemons to load so the empty-state CTA appears.
    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations",
    });
    // Sidebar starts open; close it to expose the empty-state CTA.
    fireEvent.click(screen.getByRole("button", { name: "Hide conversations" }));
    expect(
      screen.getByRole("button", { name: "Show conversations" }),
    ).toBeInTheDocument();
    expect(sidebar.className).toContain("lg:hidden");

    // The empty state exposes its own "New conversation" shortcut, which opens
    // the create form as a centered dialog regardless of the sidebar state.
    const newConversationButtons = screen.getAllByRole("button", {
      name: "New conversation",
    });
    fireEvent.click(newConversationButtons[newConversationButtons.length - 1]);

    const dialog = await screen.findByRole("dialog", {
      name: "New conversation",
    });
    expect(within(dialog).getByLabelText("Agent daemon")).toBeInTheDocument();
  });

  it("renders the list view when the route has no selected conversation", async () => {
    const api = makeApi({
      conversations: [
        {
          id: "conv_one",
          title: "Only chat",
          status: "active",
          updatedAt: "2026-06-21T16:00:00Z",
        },
      ],
    });

    renderWithClient(
      <AgentsTab
        api={api}
        conversationId=""
        slice={{ account: "nic", slice: "home" }}
      />,
    );

    const sidebar = await screen.findByRole("complementary", {
      name: "Agent conversations",
    });
    expect(
      within(sidebar).getByRole("button", { name: /Only chat/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Only chat" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Conversations" }),
    ).not.toBeInTheDocument();
  });

  it("renders the selected conversation with a mobile back button", async () => {
    const onBack = vi.fn();
    const api = makeApi({
      conversations: [
        {
          id: "conv_one",
          title: "Only chat",
          status: "active",
          updatedAt: "2026-06-21T16:00:00Z",
        },
      ],
    });

    renderWithClient(
      <AgentsTab
        api={api}
        conversationId="conv_one"
        onBack={onBack}
        slice={{ account: "nic", slice: "home" }}
      />,
    );

    expect(
      await screen.findByRole("heading", { name: "Only chat" }),
    ).toBeInTheDocument();
    const backButton = screen.getByRole("button", {
      name: "← Conversations",
    });
    expect(backButton).toBeInTheDocument();

    fireEvent.click(backButton);

    expect(onBack).toHaveBeenCalledTimes(1);
    expect(
      screen.queryByRole("heading", { name: "Only chat" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("complementary", { name: "Agent conversations" }),
    ).toBeInTheDocument();
  });
});

// react-query requires a provider; tests that previously rendered AgentsTab
// directly now go through this helper. The client is configured to fail fast
// so mutation/query errors don't get retried into flaky timeouts.
function renderWithClient(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false, gcTime: 0, staleTime: 0 },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>,
  );
}

function makeApi(
  overrides: {
    conversations?: Array<{
      id: string;
      title: string;
      status: string;
      createdAt?: string;
      updatedAt?: string;
    }>;
    onCreate?: () => Conversation;
  } = {},
) {
  const conversations: Conversation[] = (overrides.conversations ?? []).map(
    (c) => ({
      id: c.id,
      title: c.title,
      status: c.status,
      createdAt: c.createdAt,
      updatedAt: c.updatedAt,
    }),
  );
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
          createdAt: "2026-06-21T00:00:00Z",
        },
      ],
    }),
    listConversations: vi.fn().mockResolvedValue({ conversations }),
    createConversation: vi.fn().mockImplementation(async () => {
      if (overrides.onCreate) {
        return overrides.onCreate();
      }
      throw new Error("createConversation not stubbed");
    }),
    getConversation: vi.fn(),
    sendAgentMessage: vi.fn(),
    streamConversation: vi.fn(async function* () {}),
  } as unknown as ApiClient;
}
