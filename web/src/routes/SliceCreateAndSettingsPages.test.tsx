import "@testing-library/jest-dom/vitest";

import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SliceCreatePage } from "./SliceCreatePage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import {
  apiMock,
  authMock,
  cleanupSlicePagesHooks,
  expectHealthy,
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

describe("slice creation and settings route pages", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleError = resetSlicePagesHooks();
  });

  afterEach(() => {
    cleanupSlicePagesHooks(consoleError);
  });

  it("creates a slice in an organization the viewer administers", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice", "acme"],
      memberships: [
        ...selectionMock.current.memberships,
        { account: "gitslice", kind: "organization", role: "owner" },
        { account: "acme", kind: "organization", role: "reader" }
      ]
    };
    routerMock.search = { account: "gitslice" };
    renderSliceRoute(<SliceCreatePage />);
    await flushAsync();
    const select = screen.getByRole("combobox");
    expect(select).toHaveValue("gitslice");
    // Only accounts you own or administer are offered.
    expect(within(select).getAllByRole("option").map((o) => o.getAttribute("value"))).toEqual(["nic", "gitslice"]);
  });

  it("renders the slice settings page", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    renderSliceRoute(<SliceSettingsPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expectHealthy(consoleError);
  });

  it("updates the slice CI daemon from settings", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const api = makeSlicePagesApi();
    const slice = {
      id: "slice_nic_home",
      ref: { account: "nic", slice: "home" },
      definition: {
        sliceId: "slice_nic_home",
        version: "2",
        includedPaths: ["/nic"],
        visibility: "public",
        requiredApprovals: 0,
        requiredChecks: []
      },
      definitionHash: "sha256:abc",
      ciDaemonId: "daemon_1"
    };
    api.resolveSlice = vi.fn().mockResolvedValue(slice);
    api.listDaemons = vi.fn().mockResolvedValue({
      daemons: [
        {
          id: "daemon_1",
          name: "runner-one",
          runtime: "codex",
          status: "online",
          version: "1.0.0"
        },
        {
          id: "daemon_2",
          name: "runner-two",
          runtime: "codex",
          status: "online",
          version: "1.0.1"
        },
        {
          id: "daemon_3",
          name: "offline-runner",
          runtime: "codex",
          status: "offline",
          version: "1.0.0"
        }
      ]
    });
    api.setSliceCIDaemon = vi.fn().mockResolvedValue({
      ...slice,
      ciDaemonId: "daemon_2"
    });
    apiMock.current = api;

    renderSliceRoute(<SliceSettingsPage />);

    expect(await screen.findByText("daemon_1")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("CI daemon"), {
      target: { value: "daemon_2" }
    });

    await waitFor(() =>
      expect(api.setSliceCIDaemon).toHaveBeenCalledWith({
        daemonId: "daemon_2",
        slice: { account: "nic", slice: "home" }
      })
    );
  });

  it("adds a backup CI daemon and removes one from the pool", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const api = makeSlicePagesApi();
    const slice = {
      id: "slice_nic_home",
      ref: { account: "nic", slice: "home" },
      definition: { sliceId: "slice_nic_home", version: "2", includedPaths: ["/nic"], visibility: "private" },
      ciDaemonId: "daemon_1",
      ciDaemonName: "runner-one",
      ciDaemonIds: ["daemon_1", "daemon_3"],
      ciDaemonNames: ["runner-one", "offline-runner"]
    };
    api.resolveSlice = vi.fn().mockResolvedValue(slice);
    api.listDaemons = vi.fn().mockResolvedValue({
      daemons: [
        { id: "daemon_1", name: "runner-one", runtime: "ci", status: "online" },
        { id: "daemon_2", name: "runner-two", runtime: "ci", status: "online" },
        { id: "daemon_3", name: "offline-runner", runtime: "ci", status: "offline" }
      ]
    });
    api.setSliceCIDaemon = vi.fn().mockResolvedValue(slice);
    apiMock.current = api;

    renderSliceRoute(<SliceSettingsPage />);

    expect(await screen.findByText("Pool")).toBeInTheDocument();
    expect(screen.getByText("2. offline-runner")).toBeInTheDocument();
    const backup = await screen.findByLabelText("Add a backup CI daemon");
    // Only online daemons not in the pool can be added.
    expect(within(backup).queryByText(/runner-one/)).not.toBeInTheDocument();
    fireEvent.change(backup, { target: { value: "daemon_2" } });
    await waitFor(() =>
      expect(api.setSliceCIDaemon).toHaveBeenCalledWith({
        add: true,
        daemonId: "daemon_2",
        slice: { account: "nic", slice: "home" }
      })
    );
    fireEvent.click(screen.getAllByRole("button", { name: "Remove" })[1]);
    await waitFor(() =>
      expect(api.setSliceCIDaemon).toHaveBeenLastCalledWith({
        daemonId: "daemon_3",
        remove: true,
        slice: { account: "nic", slice: "home" }
      })
    );
  });

  it("names the slice's CI daemon, even for someone who cannot list it", async () => {
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_nic_home",
      ref: { account: "nic", slice: "home" },
      definition: {
        sliceId: "slice_nic_home",
        version: "2",
        includedPaths: ["/nic"],
        visibility: "public",
        requiredApprovals: 0,
        requiredChecks: []
      },
      definitionHash: "sha256:abc",
      ciDaemonId: "agent_f83ad2fa",
      ciDaemonName: "Main CI"
    });
    // The viewer lists no daemons: the agent that runs CI is not theirs.
    api.listDaemons = vi.fn().mockResolvedValue({ daemons: [] });
    apiMock.current = api;

    renderSliceRoute(<SliceSettingsPage />);

    expect(await screen.findByText("Main CI")).toBeInTheDocument();
    expect(screen.getByText("agent_f83ad2fa")).toBeInTheDocument();
  });

  it("renders the new slice page", async () => {
    renderSliceRoute(<SliceCreatePage />);
    await flushAsync();
    expectHealthy(consoleError);
  });
});
