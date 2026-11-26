import "@testing-library/jest-dom/vitest";

import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../api/client";
import { AccountPage } from "./AccountPage";
import { HomePage } from "./HomePage";
import { NewOrganizationPage } from "./NewOrganizationPage";
import {
  apiMock,
  authMock,
  cleanupSlicePagesHooks,
  expectHealthy,
  featuresMock,
  flushAsync,
  makeSlicePagesApi,
  renderSliceRoute,
  resetSlicePagesHooks,
  routerMock,
  selectionMock
} from "./slicePagesTestHelpers";

vi.mock("../lib/features", async () => {
  const { featuresMock } = await import("./slicePagesTestHelpers");
  return {
    get conversationsEnabled() {
      return featuresMock.conversationsEnabled;
    }
  };
});

vi.mock("../api/useApi", async () => {
  const { apiMock } = await import("./slicePagesTestHelpers");
  return { useApi: () => apiMock.current };
});

vi.mock("@clerk/tanstack-react-start", async () => {
  const { authMock } = await import("./slicePagesTestHelpers");
  return { useAuth: () => authMock.current };
});

vi.mock("@tanstack/react-router", async () => {
  const { makeRouterLink, routerMock } = await import("./slicePagesTestHelpers");
  return {
    Link: makeRouterLink,
    useNavigate: () => routerMock.navigate,
    useParams: () => routerMock.params,
    useRouter: () => ({ history: { back: routerMock.back, push: routerMock.push } }),
    useSearch: () => routerMock.search
  };
});

vi.mock("../state/selection", async () => {
  const { selectionMock } = await import("./slicePagesTestHelpers");
  return { useSelection: () => selectionMock.current };
});

describe("home, account, and organization route pages", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleError = resetSlicePagesHooks();
  });

  afterEach(() => {
    cleanupSlicePagesHooks(consoleError);
  });

  it("renders the slices list", async () => {
    renderSliceRoute(<HomePage />);
    await waitFor(() => expect(apiMock.current.listSlices).toHaveBeenCalled());
    await flushAsync();
    expectHealthy(consoleError);
  });

  it("offers no conversations on Home while they are not offered and shows a single Manage account link", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const api = makeSlicePagesApi();
    api.listConversations = vi.fn().mockResolvedValue({
      conversations: [{ id: "c1", title: "Personal chat", slice: { account: "nic", slice: "home" }, createdAt: "2026-10-01T00:00:00Z" }]
    });
    apiMock.current = api;
    renderSliceRoute(<HomePage />);
    await waitFor(() => expect(api.listSlices).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "New conversation" })).toBeNull();
    expect(screen.queryByText("Personal chat")).toBeNull();
    expect(api.listConversations).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Manage account" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Account & runners" })).not.toBeInTheDocument();
  });

  it("sends Home in an organization's context to its profile page", () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const acme = { account: "acme", kind: "organization", role: "writer" };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      activeAccount: "acme",
      activeMembership: acme,
      memberships: [...selectionMock.current.memberships, acme]
    };
    apiMock.current = makeSlicePagesApi();
    renderSliceRoute(<HomePage />);

    expect(routerMock.navigate).toHaveBeenCalledWith({ to: "/accounts/$account", params: { account: "acme" }, replace: true });
    expect(screen.queryByRole("heading", { name: "acme · Organization" })).not.toBeInTheDocument();
  });

  it("scopes Home to the active organization and offers invitations", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const setActiveAccount = vi.fn();
    const acme = { account: "acme", kind: "organization", role: "writer" };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      activeAccount: "acme",
      activeMembership: acme,
      memberships: [...selectionMock.current.memberships, acme],
      setActiveAccount
    };
    featuresMock.conversationsEnabled = true;
    const api = makeSlicePagesApi();
    api.listConversations = vi.fn().mockResolvedValue({
      conversations: [
        { id: "c1", title: "Personal chat", slice: { account: "nic", slice: "home" }, createdAt: "2026-10-01T00:00:00Z" },
        { id: "c2", title: "Team chat", slice: { account: "acme", slice: "payment" }, createdAt: "2026-10-02T00:00:00Z" }
      ]
    });
    api.listMyInvitations = vi.fn().mockResolvedValue({ invitations: [{ account: "labs", role: "reader", invitedBy: "boss" }] });
    apiMock.current = api;
    renderSliceRoute(<HomePage />);

    expect(await screen.findByText("Team chat")).toBeInTheDocument();
    expect(screen.queryByText("Personal chat")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "acme · Organization" })).toBeInTheDocument();
    expect(api.listPendingClaims).not.toHaveBeenCalled();
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "acme" }));

    const invitations = await screen.findByRole("region", { name: "You are invited to an organization" });
    fireEvent.click(within(invitations).getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(api.respondToInvitation).toHaveBeenCalledWith({ account: "labs", accept: true }));
    await waitFor(() => expect(setActiveAccount).toHaveBeenCalledWith("labs"));
  });

  it("creates an organization and switches to it", async () => {
    const setActiveAccount = vi.fn();
    selectionMock.current = { ...selectionMock.current, setActiveAccount };
    const api = makeSlicePagesApi();
    api.createOrganization = vi.fn().mockResolvedValue({ account: "acme-labs" });
    apiMock.current = api;
    renderSliceRoute(<NewOrganizationPage />);

    const input = screen.getByPlaceholderText("acme-labs");
    fireEvent.change(input, { target: { value: "ab" } });
    fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
    expect(await screen.findByText(/Use 4 to 63 letters/)).toBeInTheDocument();
    expect(api.createOrganization).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: "Acme_Labs" } });
    fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
    await waitFor(() => expect(api.createOrganization).toHaveBeenCalledWith({ slug: "acme-labs" }));
    await waitFor(() => expect(setActiveAccount).toHaveBeenCalledWith("acme-labs"));
    await waitFor(() =>
      expect(routerMock.navigate).toHaveBeenCalledWith({ params: { account: "acme-labs" }, to: "/accounts/$account" })
    );
  });

  it("shows an account's profile and lets its admins edit it", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice"],
      memberships: [...selectionMock.current.memberships, { account: "gitslice", kind: "organization", role: "owner" }]
    };
    routerMock.params = { account: "gitslice" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.getAccountProfile = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      displayName: "Gitslice",
      description: "Slices of a source graph.",
      website: "https://gitslice.io"
    });
    api.updateAccountProfile = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      displayName: "Gitslice HQ",
      description: "Slices of a source graph.",
      website: "https://gitslice.io"
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByRole("heading", { name: "Gitslice" })).toBeInTheDocument();
    expect(screen.getByText("Slices of a source graph.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "gitslice.io" })).toHaveAttribute("rel", "noopener noreferrer nofollow ugc");

    fireEvent.click(screen.getByRole("button", { name: "Edit profile" }));
    const form = screen.getByRole("form", { name: "Edit profile" });
    fireEvent.change(within(form).getByLabelText("Display name"), { target: { value: "Gitslice HQ" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    await waitFor(() =>
      expect(api.updateAccountProfile).toHaveBeenCalledWith({
        account: "gitslice",
        description: "Slices of a source graph.",
        displayName: "Gitslice HQ",
        website: "https://gitslice.io"
      })
    );
    expect(await screen.findByRole("heading", { name: "Gitslice HQ" })).toBeInTheDocument();
  });

  it("uploads and removes an account's picture, and shows it at its new address at once", async () => {
    routerMock.params = { account: "nic" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "personal" });
    api.getAccountProfile = vi.fn().mockResolvedValue({ account: "nic", kind: "personal" });
    api.setAccountAvatar = vi
      .fn()
      .mockResolvedValueOnce({ account: "nic", kind: "personal", avatarVersion: "0123abcd0123abcd" })
      .mockResolvedValueOnce({ account: "nic", kind: "personal", avatarVersion: "" });
    apiMock.current = api;
    const { container } = renderSliceRoute(<AccountPage />);

    // Before an upload, the generated picture.
    await waitFor(() => expect(container.querySelector('img[src="/avatars/nic.png?v=none"]')).not.toBeNull());
    // The profile header shows it large (80px from sm up, 64px on phones).
    expect(container.querySelector('img[src="/avatars/nic.png?v=none"]')).toHaveClass("size-16", "sm:size-20");
    fireEvent.click(await screen.findByRole("button", { name: "Edit profile" }));
    const form = screen.getByRole("form", { name: "Edit profile" });
    expect(within(form).queryByRole("button", { name: "Remove picture" })).toBeNull();

    // Not a picture: refused before uploading.
    const input = within(form).getByLabelText("Picture file");
    fireEvent.change(input, { target: { files: [new File(["<svg/>"], "x.svg", { type: "image/svg+xml" })] } });
    expect(await within(form).findByText("Choose a PNG, JPEG, GIF or WebP picture.")).toBeInTheDocument();
    expect(api.setAccountAvatar).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "me.png", { type: "image/png" })] } });
    await waitFor(() =>
      expect(api.setAccountAvatar).toHaveBeenCalledWith({ account: "nic", image: new Uint8Array([137, 80, 78, 71]) })
    );
    // Every avatar of the account switches to the uploaded picture.
    await waitFor(() => expect(container.querySelectorAll('img[src="/avatars/nic.png?v=0123abcd0123abcd"]').length).toBeGreaterThanOrEqual(2));
    expect(container.querySelector('img[src="/avatars/nic.png?v=none"]')).toBeNull();

    fireEvent.click(within(form).getByRole("button", { name: "Remove picture" }));
    await waitFor(() => expect(api.setAccountAvatar).toHaveBeenLastCalledWith({ account: "nic", image: undefined }));
    await waitFor(() => expect(container.querySelector('img[src="/avatars/nic.png?v=none"]')).not.toBeNull());
  });

  it("lists your organizations on Overview and your agents and CI runners in their tabs on your own page", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice", "heibot"],
      memberships: [
        ...selectionMock.current.memberships,
        { account: "gitslice", kind: "organization", role: "owner" },
        { account: "heibot", kind: "agent", role: "owner" }
      ]
    };
    routerMock.params = { account: "nic" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "personal" });
    api.getAccountProfile = vi.fn().mockResolvedValue({ account: "nic", kind: "personal" });
    api.listOwnedAgents = vi.fn().mockResolvedValue({
      agents: [{ account: "heibot", agentDisplayName: "hei", agentSubjectId: "agent_1", claimedAt: "2026-09-30T02:01:48Z" }]
    });
    api.getAccountCIDaemons = vi.fn().mockResolvedValue({ account: "nic", daemonIds: [], daemonNames: [] });
    api.listDaemons = vi.fn().mockResolvedValue({
      daemons: [{ id: "d1", account: "nic", name: "mac-ci", runtime: "ci", version: "0.4.34", status: "online", mine: true }]
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    const orgs = await screen.findByRole("region", { name: "Organizations" });
    expect(within(orgs).getByRole("link", { name: /gitslice/ })).toBeInTheDocument();
    expect(within(orgs).getByText("owner")).toBeInTheDocument();
    expect(within(orgs).getByRole("link", { name: "New organization" })).toBeInTheDocument();
    // Agents and CI runners stay tucked in their dedicated tabs instead of crowding Overview.
    expect(screen.queryByRole("region", { name: "Your agents" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Registered CI runners" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Agents" }));
    expect(routerMock.navigate).toHaveBeenCalledWith({
      params: { account: "nic" },
      replace: true,
      search: { tab: "agents" },
      to: "/accounts/$account"
    });
    const agents = await screen.findByRole("region", { name: "Your agents" });
    expect(within(agents).getByText("hei")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "CI Runners" }));
    expect(routerMock.navigate).toHaveBeenCalledWith({
      params: { account: "nic" },
      replace: true,
      search: { tab: "runners" },
      to: "/accounts/$account"
    });
    expect(await screen.findByRole("region", { name: "Default CI runner pool" })).toBeInTheDocument();
    expect(await screen.findByText("mac-ci")).toBeInTheDocument();
  });

  it("does not offer to edit someone else's profile", async () => {
    routerMock.params = { account: "stranger" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "personal" });
    api.getAccountProfile = vi.fn().mockResolvedValue({ account: "stranger", kind: "personal" });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText(/Personal/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit profile" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "People" })).not.toBeInTheDocument();
  });

  it("lists an account's slices on its page", async () => {
    routerMock.params = { account: "gitslice" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({
      slices: [
        { id: "s1", ref: { account: "gitslice", slice: "gitslice" }, definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" } },
        { id: "s2", ref: { account: "gitslice", slice: "docs" }, definition: { includedPaths: ["/gitslice/docs"], visibility: "public" } }
      ]
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("docs")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "gitslice" }));
    expect(screen.getByRole("heading", { name: "gitslice" })).toBeInTheDocument();
    expect(screen.getByText("Public slices owned by gitslice.")).toBeInTheDocument();
    expectHealthy(consoleError);
  });

  it("features the home monorepo above slice views and sorts the views as the viewer picks", async () => {
    routerMock.params = { account: "sunny" };
    const api = makeSlicePagesApi();
    const slice = (name: string, path: string, createdAt: string, lastActivityAt: string) => ({
      id: name,
      ref: { account: "sunny", slice: name },
      definition: { includedPaths: [path], visibility: "public" },
      createdAt,
      lastActivityAt
    });
    api.listSlices = vi.fn().mockResolvedValue({
      slices: [
        slice("botparley", "/sunny/botparley", "2026-10-01T00:00:00Z", "2026-10-30T00:00:00Z"),
        slice("home", "/sunny", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z"),
        slice("rental-price", "/sunny/pricing-service", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z")
      ]
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    await screen.findByText("botparley");
    const monorepo = screen.getByRole("region", { name: "Monorepo" });
    expect(within(monorepo).getByRole("link", { name: "Browse /sunny" })).toBeInTheDocument();
    expect(within(monorepo).getByRole("link", { name: "Folder policy" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Slices (Views)" })).toBeInTheDocument();

    const names = () =>
      screen
        .getAllByRole("listitem")
        .map((item) => item.querySelector("span span span")?.textContent)
        .filter((name) => ["home", "botparley", "rental-price"].includes(name ?? ""));
    expect(names()).toEqual(["botparley", "rental-price"]);

    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "newest" } });
    await waitFor(() => expect(names()).toEqual(["rental-price", "botparley"]));
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "sunny", sort: "newest" }));
    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "active" } });
    await waitFor(() => expect(names()).toEqual(["botparley", "rental-price"]));
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "sunny", sort: "active" }));
    expect(window.localStorage.getItem("gitslice.sliceSort")).toBe("active");
    window.localStorage.removeItem("gitslice.sliceSort");
  });

  it("paginates server-sorted slices without fetching every page upfront", async () => {
    routerMock.params = { account: "big" };
    window.localStorage.setItem("gitslice.sliceSort", "newest");
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockImplementation(async (req: { cursor?: string; sort?: string }) => {
      if (req.sort !== "newest") {
        return {
          slices: [{ id: "b", ref: { account: "big", slice: "bravo" }, definition: { visibility: "public" }, createdAt: "2026-02-01T00:00:00Z" }],
          nextCursor: "v1|0|1|bravo"
        };
      }
      if (!req.cursor) {
        return {
          slices: [{ id: "b", ref: { account: "big", slice: "bravo" }, definition: { visibility: "public" }, createdAt: "2026-02-01T00:00:00Z" }],
          nextCursor: "v1|0|1|bravo"
        };
      }
      return {
        slices: [{ id: "a", ref: { account: "big", slice: "alpha" }, definition: { visibility: "public" }, createdAt: "2026-01-01T00:00:00Z" }]
      };
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("bravo")).toBeInTheDocument();
    await waitFor(() =>
      expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "big", cursor: "", sort: "newest" }))
    );
    expect(screen.queryByText("alpha")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("alpha")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenLastCalledWith(
      expect.objectContaining({ account: "big", cursor: "v1|0|1|bravo", sort: "newest" })
    );
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    window.localStorage.removeItem("gitslice.sliceSort");
  });

  it("pages through a long list of slices", async () => {
    routerMock.params = { account: "big" };
    const api = makeSlicePagesApi();
    api.listSlices = vi
      .fn()
      .mockResolvedValueOnce({
        slices: [{ id: "a", ref: { account: "big", slice: "alpha" }, definition: { visibility: "public" } }],
        nextCursor: "alpha"
      })
      .mockResolvedValueOnce({
        slices: [{ id: "b", ref: { account: "big", slice: "bravo" }, definition: { visibility: "public" } }]
      });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("alpha")).toBeInTheDocument();
    expect(screen.queryByText("bravo")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("bravo")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "alpha" }));
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  it("does not retry an answer that will not change", async () => {
    routerMock.params = { account: "gitslice" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockRejectedValue(new RpcError(401, { code: "unauthenticated", message: "missing subject" }));
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("Could not load slices")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledTimes(1);
  });

  it("says when the account does not exist", async () => {
    routerMock.params = { account: "ghost" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockRejectedValue(new RpcError(404, { code: 5, message: "not found" }));
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("No such account")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledTimes(1);
  });

  it("tells a member of an organization that its page shows everything to them", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "writer" }]
    };
    routerMock.params = { account: "acme" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [] });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText(/Slices of acme, which you belong to/)).toBeInTheDocument();
  });

  it("lets an organization owner manage its people", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice"],
      memberships: [...selectionMock.current.memberships, { account: "gitslice", kind: "organization", role: "owner" }]
    };
    routerMock.params = { account: "gitslice" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.listAccountMembers = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      members: [
        { username: "nic", subjectId: "user_1", role: "owner" },
        { username: "gitslice-mirror", subjectId: "agent_2", role: "writer" }
      ]
    });
    api.setAccountMember = vi.fn().mockResolvedValue({});
    api.removeAccountMember = vi.fn().mockResolvedValue({});
    api.inviteAccountMember = vi.fn().mockResolvedValue({});
    api.cancelAccountInvitation = vi.fn().mockResolvedValue({});
    api.listAccountInvitations = vi.fn().mockResolvedValue({
      invitations: [{ account: "gitslice", username: "pending-pat", role: "reader", invitedBy: "nic" }]
    });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    fireEvent.click(await screen.findByRole("tab", { name: "People" }));
    const people = await screen.findByRole("region", { name: "People" });
    expect(await within(people).findByText("gitslice-mirror")).toBeInTheDocument();
    expect(screen.getByText(/Organization · you are an owner/)).toBeInTheDocument();

    fireEvent.change(within(people).getByLabelText("Role of gitslice-mirror"), { target: { value: "reader" } });
    await waitFor(() =>
      expect(api.setAccountMember).toHaveBeenCalledWith({ account: "gitslice", role: "reader", username: "gitslice-mirror" })
    );

    fireEvent.click(within(people).getAllByRole("button", { name: "Remove" })[1]);
    await waitFor(() => expect(api.removeAccountMember).toHaveBeenCalledWith({ account: "gitslice", username: "gitslice-mirror" }));

    // New people are invited, not added.
    fireEvent.change(within(people).getByPlaceholderText("username"), { target: { value: "@alice" } });
    fireEvent.click(within(people).getByRole("button", { name: "Invite" }));
    await waitFor(() =>
      expect(api.inviteAccountMember).toHaveBeenLastCalledWith({ account: "gitslice", role: "writer", username: "alice" })
    );
    expect(await within(people).findByText("pending-pat")).toBeInTheDocument();
    fireEvent.click(within(people).getByRole("button", { name: "Cancel invitation" }));
    await waitFor(() =>
      expect(api.cancelAccountInvitation).toHaveBeenCalledWith({ account: "gitslice", username: "pending-pat" })
    );
  });

  it("shows a reader the people but not the controls", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "reader" }]
    };
    routerMock.params = { account: "acme" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.listAccountMembers = vi.fn().mockResolvedValue({ members: [{ username: "boss", role: "owner" }] });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    fireEvent.click(await screen.findByRole("tab", { name: "People" }));
    const people = await screen.findByRole("region", { name: "People" });
    expect(await within(people).findByText("boss")).toBeInTheDocument();
    expect(within(people).queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(within(people).queryByPlaceholderText("username")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New slice" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "CI Runners" })).not.toBeInTheDocument();
  });

  it("says when an account has no public slices", async () => {
    routerMock.params = { account: "quiet" };
    const api = makeSlicePagesApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [] });
    apiMock.current = api;
    renderSliceRoute(<AccountPage />);

    expect(await screen.findByText("No public slices")).toBeInTheDocument();
  });
});
