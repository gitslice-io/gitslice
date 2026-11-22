import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../api/rpcError";
import { ChangesetDetailPage } from "./ChangesetDetailPage";
import {
  apiMock,
  changeset,
  cleanupChangesetTestHooks,
  featuresMock,
  makeChangesetApi,
  renderChangesetRoute,
  resetChangesetTestHooks,
  routerMock
} from "./changesetDetailTestHelpers";

vi.mock("../lib/features", async () => {
  const { featuresMock } = await import("./changesetDetailTestHelpers");
  return {
    get conversationsEnabled() {
      return featuresMock.conversationsEnabled;
    }
  };
});

vi.mock("../api/useApi", async () => {
  const { apiMock } = await import("./changesetDetailTestHelpers");
  return {
    useApi: () => apiMock.current
  };
});

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({
    isLoaded: true,
    isSignedIn: true
  })
}));

vi.mock("@tanstack/react-router", async () => {
  const [React, { makeRouterLink, routerMock }] = await Promise.all([
    import("react"),
    import("./changesetDetailTestHelpers")
  ]);
  return {
    Link: makeRouterLink,
    useNavigate: () => routerMock.navigate,
    useParams: () => routerMock.params,
    useRouter: () => ({ history: { back: routerMock.back, push: vi.fn() } }),
    useSearch: () =>
      React.useSyncExternalStore(
        routerMock.subscribe,
        () => routerMock.search,
        () => routerMock.search
      )
  };
});

vi.mock("../state/selection", async () => {
  const { selectionMock } = await import("./changesetDetailTestHelpers");
  return {
    useSelection: () => selectionMock.current
  };
});

vi.mock("../components/diff/DiffViewer", async () => {
  const { MockDiffViewer } = await import("./changesetDetailTestHelpers");
  return {
    DiffViewer: MockDiffViewer
  };
});

describe("changeset detail links, errors, and conversation drawer", () => {
  beforeEach(() => resetChangesetTestHooks());
  afterEach(() => cleanupChangesetTestHooks());

  it("moves a link made from a landed commit's id to the changeset's own link", async () => {
    routerMock.params = { id: "2d15cca3e1b1" };
    const api = makeChangesetApi();
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi
      .fn()
      .mockResolvedValue(changeset("cs_cf52df6ccbcb849a1b29b76c6b98335f", "add expenses"));
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(await screen.findByText("add expenses")).toBeInTheDocument();
    expect(api.getChangeset).toHaveBeenCalledWith({ changesetId: "2d15cca3e1b1" });
    await waitFor(() =>
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ params: { id: "cf52df6ccb" }, replace: true, to: "/cs/$id" })
      )
    );
  });

  it("says why a changeset did not load", async () => {
    for (const [status, title, action] of [
      [404, "No changeset 2d15cca3e1b1", null],
      [403, "You can't see this changeset", "Claim agents"],
      [401, "Sign in to see this changeset", "Sign in"],
      [500, "Unable to load changeset", null]
    ] as const) {
      routerMock.params = { id: "2d15cca3e1b1" };
      const api = makeChangesetApi();
      api.getChangeset = vi.fn().mockRejectedValue(new RpcError(status, { message: "nope" }));
      apiMock.current = api;
      renderChangesetRoute(<ChangesetDetailPage />);
      expect(await screen.findByRole("heading", { name: title })).toBeInTheDocument();
      if (action) {
        expect(screen.getByRole("link", { name: action })).toBeInTheDocument();
      }
      cleanup();
    }
  });

  it("keeps a changeset's own link", async () => {
    routerMock.params = { id: "cf52df6ccb" };
    const api = makeChangesetApi();
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi
      .fn()
      .mockResolvedValue(changeset("cs_cf52df6ccbcb849a1b29b76c6b98335f", "add expenses"));
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(await screen.findByText("add expenses")).toBeInTheDocument();
    expect(routerMock.navigate).not.toHaveBeenCalledWith(expect.objectContaining({ to: "/cs/$id" }));
  });

  it("names the browser tab after the changeset once it has loaded", async () => {
    routerMock.params = { id: "cf52df6ccb" };
    const api = makeChangesetApi();
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi
      .fn()
      .mockResolvedValue(changeset("cs_cf52df6ccbcb849a1b29b76c6b98335f", "add expenses"));
    apiMock.current = api;
    document.title = "Gitslice";

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(await screen.findByText("add expenses")).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("add expenses · acme/payment · Gitslice"));
  });

  it("has no conversation toggle or drawer while conversations are not offered, even from an old link", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = { conversation: "1" };
    const api = makeChangesetApi();
    apiMock.current = api;
    renderChangesetRoute(<ChangesetDetailPage />);
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByRole("button", { name: "Toggle conversation for the selected patchset" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Agent conversation" })).toBeNull();
  });

  it("opens the conversation drawer from the URL", async () => {
    featuresMock.conversationsEnabled = true;
    routerMock.params = { id: "cs_child" };
    routerMock.search = { conversation: "1" };
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      conversationId: "conv_child",
      conversationSeq: "7",
      patchsetId: "ps_child_1",
      patchsetNumber: "1"
    });
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.getConversationEvents = vi.fn().mockResolvedValue({
      events: [
        {
          conversationId: "conv_child",
          id: "event_1",
          role: "assistant",
          seq: "7",
          text: "Updated the parser.",
          type: "message"
        }
      ]
    });
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(
      await screen.findByRole("heading", { name: "Agent conversation" })
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(api.getConversationEvents).toHaveBeenCalledWith({
        conversationId: "conv_child",
        afterSeq: 0,
        beforeSeq: 7
      })
    );
    expect(await screen.findByText("Updated the parser.")).toBeInTheDocument();
  });

  it("preserves existing changeset search params when opening the conversation drawer", async () => {
    featuresMock.conversationsEnabled = true;
    routerMock.params = { id: "cs_child" };
    routerMock.search = {
      file: "/acme/payment/parser.go",
      from: "ps_child_1",
      to: "ps_child_2"
    };
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      conversationId: "conv_child",
      conversationSeq: "9",
      patchsetId: "ps_child_2",
      patchsetNumber: "2"
    });
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        authoringConversationId: "conv_child",
        authoringConversationSeq: "3",
        createdAt: "2026-06-18T00:00:00Z",
        id: "ps_child_1",
        number: "1"
      },
      {
        ...(detail.patchsets?.[0] ?? {}),
        authoringConversationId: "conv_child",
        authoringConversationSeq: "9",
        createdAt: "2026-06-18T00:01:00Z",
        id: "ps_child_2",
        number: "2"
      }
    ];
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await screen.findByText("use parser in payment API");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Toggle conversation for the selected patchset"
      })
    );

    expect(routerMock.search).toEqual({
      conversation: true,
      file: "/acme/payment/parser.go",
      from: "ps_child_1",
      to: "ps_child_2"
    });

    fireEvent.click(
      screen.getByRole("button", {
        name: "Toggle conversation for the selected patchset"
      })
    );
    expect(routerMock.back).toHaveBeenCalledTimes(1);
  });
});
